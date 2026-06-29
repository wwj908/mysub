package main

import (
	"log"

	"github.com/alicebob/miniredis/v2"
)

func main() {
	s := miniredis.NewMiniRedis()
	if err := s.StartAddr("127.0.0.1:6379"); err != nil {
		log.Fatalf("failed to start miniredis: %v", err)
	}
	log.Printf("miniredis listening on %s", s.Addr())
	select {}
}
