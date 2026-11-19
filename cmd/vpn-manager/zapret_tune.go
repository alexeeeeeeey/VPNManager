package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/api"
)

func (s server) TuneZapret(w http.ResponseWriter, r *http.Request) {
	var input api.ZapretTuneInput
	if !decode(w, r, &input) {
		return
	}
	if len(input.Targets) < 1 || len(input.Targets) > 5 {
		jsonResponse(w, nil, errors.New("укажите от одного до пяти доменов"))
		return
	}
	result := api.ZapretTuneResult{Targets: make([]string, 0, len(input.Targets)), Best: -1}
	seen := make(map[string]bool)
	for _, target := range input.Targets {
		address, err := zapretTarget(target)
		if err != nil {
			jsonResponse(w, nil, fmt.Errorf("%q: %w", target, err))
			return
		}
		if !seen[address] {
			seen[address] = true
			result.Targets = append(result.Targets, address)
		}
	}
	if !s.writes.TryLock() {
		jsonResponse(w, nil, errors.New("другая операция уже выполняется"))
		return
	}
	defer s.writes.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	state, err := s.loadZapret(ctx)
	if err != nil {
		jsonResponse(w, nil, err)
		return
	}
	if state.Version == "" || zapretArchitecture() == "" {
		jsonResponse(w, nil, errors.New("для автоподбора сначала установите Zapret2 из панели"))
		return
	}
	routing, err := s.routingSettings(ctx)
	if err != nil {
		jsonResponse(w, nil, err)
		return
	}
	prefix := []string{"--lua-init=@{zapret}/lua/zapret-lib.lua", "--lua-init=@{zapret}/lua/zapret-antidpi.lua",
		"--filter-l7=tls", "--payload=tls_client_hello"}
	candidates := []api.ZapretTrial{
		{Name: "TLS · Split", Args: []string{"--lua-desync=multisplit:pos=2"}},
		{Name: "TLS · Disorder", Args: []string{"--lua-desync=multidisorder:pos=2"}},
		{Name: "TLS · Fake", Args: []string{"--lua-desync=fake:blob=fake_default_tls:tcp_ts=-1000"}},
		{Name: "Zapret2 · TLS", Args: []string{"--lua-desync=fake:blob=fake_default_tls:tcp_md5:tcp_seq=-10000", "--lua-desync=multidisorder:pos=1,midsld"}},
	}
	result.Trials = append([]api.ZapretTrial{{Name: "Без стратегии", Args: []string{}, Statuses: []int{}}}, candidates...)
	settings := make([]api.ZapretSettings, len(candidates))
	for i := range settings {
		trial := &result.Trials[i+1]
		trial.Args = append(append([]string{}, prefix...), trial.Args...)
		trial.Statuses = []int{}
		state.Args = trial.Args
		settings[i], err = s.effectiveZapret(state)
		if err != nil {
			jsonResponse(w, nil, err)
			return
		}
	}
	stream := r.URL.Query().Get("stream") == "1"
	emit := func(active int, done bool, message string) {
		_ = json.NewEncoder(w).Encode(map[string]any{"result": result, "active": active, "done": done, "error": message})
		_ = http.NewResponseController(w).Flush()
	}
	if stream {
		w.Header().Set("Content-Type", "application/x-ndjson")
		emit(-1, false, "")
	}
	if err = s.runtime.RunZapretTrials(ctx, result.Targets, result.Trials, settings, routing.Queue, func(active int) {
		if stream {
			emit(active, false, "")
		}
	}); err != nil {
		if stream {
			emit(-1, true, err.Error())
			return
		}
		jsonResponse(w, nil, err)
		return
	}
	bestCount := 0
	for i, trial := range result.Trials {
		count := 0
		for _, status := range trial.Statuses {
			if status > 0 {
				count++
			}
		}
		if count > bestCount {
			bestCount, result.Best = count, i
		}
	}
	if stream {
		emit(-1, true, "")
	} else {
		jsonResponse(w, result, nil)
	}
}
