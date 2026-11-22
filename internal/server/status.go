package server

import (
	"encoding/json"

	"spindle/internal/snapshot"
)

// field reads one key of a JSON object into T; a missing key, a null or a value
// of the wrong type gives the zero value, so a frame never carries a null.
func field[T any](m map[string]json.RawMessage, k string) (v T) {
	_ = json.Unmarshal(m[k], &v)
	return v
}

func object(raw json.RawMessage) map[string]json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return map[string]json.RawMessage{}
	}
	return m
}

// buildStatus is the compact now-playing frame, made from the cached runtime
// snapshot. cliamp leaves zero values out of the snapshot, so an absent index
// on a loaded track is 0, an absent volume is 0 dB and an absent repeat is Off.
// art is the track path: a device asks /api/art/cover?path=<art>&size=N.
func buildStatus(state json.RawMessage, online bool) []byte {
	st := object(state)
	track := object(st["track"])
	path := field[string](track, "path")
	index := -1
	if path != "" || len(track) > 0 {
		index = field[int](st, "index")
	}
	playState := field[string](st, "state")
	if playState == "" {
		playState = "stopped"
	}
	repeat := field[string](st, "repeat")
	if repeat == "" {
		repeat = "Off"
	}
	b, _ := json.Marshal(map[string]any{
		"type": "status", "online": online, "state": playState,
		"title": field[string](track, "title"), "artist": field[string](track, "artist"), "album": field[string](track, "album"),
		"position": field[float64](st, "position"), "duration": field[float64](st, "duration"),
		"volume": field[float64](st, "volume"), "shuffle": field[bool](st, "shuffle"), "repeat": repeat,
		"index": index, "seekable": field[bool](st, "seekable"), "art": path,
	})
	return b
}

// buildQueueFrame is the compact queue frame: the first queueFrameRows rows
// and the total; a device pages through the rest with /api/queue?compact=1.
func buildQueueFrame(q snapshot.Queue) []byte {
	m := object(compactQueue(q, 0, queueFrameRows))
	m["type"] = json.RawMessage(`"queue"`)
	b, _ := json.Marshal(m)
	return b
}
