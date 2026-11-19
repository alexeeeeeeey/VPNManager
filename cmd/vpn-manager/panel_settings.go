package main

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

type panelSettings struct {
	Port         int  `json:"port"`
	HTTPS        bool `json:"https"`
	TLSAvailable bool `json:"tlsAvailable"`
	Editable     bool `json:"editable"`
}

func (s server) panelSettingsRoutes(router chi.Router, cfg config) {
	host, portText, _ := net.SplitHostPort(cfg.Listen)
	port, _ := strconv.Atoi(portText)
	current := panelSettings{port, cfg.TLSCert != "" && !cfg.TLSDisabled, cfg.TLSCert != "", s.panelRestart != nil}
	restarting := false
	router.Get("/api/panel/settings", func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, current, nil)
	})
	router.Put("/api/panel/settings", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Port     int    `json:"port"`
			HTTPS    bool   `json:"https"`
			Password string `json:"password"`
		}
		if !decode(w, r, &input) {
			return
		}
		s.writes.Lock()
		defer s.writes.Unlock()
		if restarting {
			jsonResponse(w, nil, errors.New("панель уже перезапускается"))
			return
		}
		if !current.Editable || input.Port < 1 || input.Port > 65535 || (input.Password != "" && (len(input.Password) < 8 || len(input.Password) > 256)) {
			jsonResponse(w, nil, errors.New("нужен порт от 1 до 65535 и пароль от 8 до 256 байт; применение требует службы под управлением procd"))
			return
		}
		next := cfg
		next.Listen = net.JoinHostPort(host, strconv.Itoa(input.Port))
		next.TLSDisabled = !input.HTTPS
		if input.HTTPS {
			if _, err := tls.LoadX509KeyPair(next.TLSCert, next.TLSKey); err != nil {
				jsonResponse(w, nil, errors.New("для HTTPS нужен настроенный сертификат и ключ"))
				return
			}
		}
		if next.Listen != cfg.Listen {
			listener, err := net.Listen("tcp", next.Listen)
			if err != nil {
				jsonResponse(w, nil, errors.New("порт недоступен"))
				return
			}
			defer listener.Close()
		}
		if input.Password != "" {
			next.Password = input.Password
		}
		file, err := os.CreateTemp(filepath.Dir(s.panelConfigPath), ".panel-config-")
		if err != nil {
			jsonResponse(w, nil, err)
			return
		}
		defer os.Remove(file.Name())
		err = json.NewEncoder(file).Encode(next)
		if err = errors.Join(err, file.Close()); err == nil {
			err = os.Rename(file.Name(), s.panelConfigPath)
		}
		if err != nil {
			jsonResponse(w, nil, err)
			return
		}
		restarting = true
		jsonResponse(w, panelSettings{input.Port, input.HTTPS, current.TLSAvailable, true}, nil)
		time.AfterFunc(300*time.Millisecond, s.panelRestart)
	})
}
