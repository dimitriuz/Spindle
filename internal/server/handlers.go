package server

import (
	"encoding/json"
	"net/http"
)

func (s *serverState) health(w http.ResponseWriter, r *http.Request) {
	status := "offline"
	if s.cfg.Cache.Online() {
		status = "online"
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"ok": "true", "player": status})
}
