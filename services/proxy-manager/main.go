package main

import (
	"log"
	"net/http"
	"os"
)

func main() {
	port := os.Getenv("HTTP_PORT")
	if port == "" {
		port = "8080"
	}
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})
	log.Printf("Service listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
