package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/alexeeeeeeey/OpenWrt-Xray-TProxy-Setup/internal/api"
)

const zapretStrategiesKey = "zapret2_strategies"
const maxZapretStrategies = 8

func validateZapretStrategies(strategies api.ZapretStrategies) error {
	if len(strategies) > maxZapretStrategies {
		return errors.New("можно выбрать или сохранить до восьми стратегий")
	}
	seen := make(map[string]bool, len(strategies))
	for _, strategy := range strategies {
		name := strings.ToLower(strategy.Name)
		if name != strings.TrimSpace(name) || !validName.MatchString(name) || name == "без стратегии" || seen[name] {
			return fmt.Errorf("некорректное или повторяющееся имя стратегии %q", strategy.Name)
		}
		seen[name] = true
		if len(strategy.Args) < 1 || len(strategy.Args) > 256 {
			return fmt.Errorf("%s: укажите от 1 до 256 аргументов", strategy.Name)
		}
		for _, arg := range strategy.Args {
			if !strings.HasPrefix(arg, "--") || len(arg) > 4096 || strings.ContainsAny(arg, "\x00\r\n") {
				return fmt.Errorf("%s: некорректный аргумент", strategy.Name)
			}
		}
	}
	return nil
}

func (s server) GetZapretStrategies(w http.ResponseWriter, r *http.Request) {
	strategies := api.ZapretStrategies{}
	err := s.readSetting(r.Context(), zapretStrategiesKey, &strategies)
	jsonResponse(w, strategies, err)
}

func (s server) PutZapretStrategies(w http.ResponseWriter, r *http.Request) {
	strategies := api.ZapretStrategies{}
	if !decode(w, r, &strategies) {
		return
	}
	if strategies == nil {
		strategies = api.ZapretStrategies{}
	}
	if err := validateZapretStrategies(strategies); err != nil {
		jsonResponse(w, nil, err)
		return
	}
	err := s.writeSetting(r.Context(), zapretStrategiesKey, strategies)
	jsonResponse(w, strategies, err)
}

func (s server) TuneZapret(w http.ResponseWriter, r *http.Request) {
	var input api.ZapretTuneInput
	if !decode(w, r, &input) {
		return
	}
	if len(input.Targets) < 1 || len(input.Targets) > 5 {
		jsonResponse(w, nil, errors.New("укажите от одного до пяти доменов"))
		return
	}
	if input.Candidates != nil {
		if len(*input.Candidates) == 0 {
			jsonResponse(w, nil, errors.New("выберите хотя бы одну стратегию"))
			return
		}
		if err := validateZapretStrategies(*input.Candidates); err != nil {
			jsonResponse(w, nil, err)
			return
		}
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
	ctx, cancel := context.WithTimeout(r.Context(), (10+8*maxZapretStrategies)*time.Second)
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
	if input.Candidates != nil {
		candidates = make([]api.ZapretTrial, 0, len(*input.Candidates))
		for _, candidate := range *input.Candidates {
			candidates = append(candidates, api.ZapretTrial{Name: candidate.Name, Args: candidate.Args})
		}
	}
	result.Trials = append([]api.ZapretTrial{{Name: "Без стратегии", Args: []string{}, Statuses: []int{}}}, candidates...)
	settings := make([]api.ZapretSettings, len(candidates))
	for i := range settings {
		trial := &result.Trials[i+1]
		if input.Candidates == nil {
			trial.Args = append(append([]string{}, prefix...), trial.Args...)
		}
		trial.Statuses = []int{}
		state.Args = trial.Args
		settings[i], err = s.effectiveZapret(state)
		if err != nil {
			trial.Error = err.Error()
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
