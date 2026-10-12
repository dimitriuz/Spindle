package server

type Hub struct{ clients map[uint64]chan Frame }

type Frame struct{ Name, Data string }

func NewHub() *Hub { return &Hub{clients: map[uint64]chan Frame{}} }
