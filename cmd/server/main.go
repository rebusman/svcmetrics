package main

import (
	"net/http"

	"github.com/rebusman/svcmetrics/internal/handler"
	"github.com/rebusman/svcmetrics/internal/storage"
)

func main() {
	s := storage.NewMemStorage()

	mux := http.NewServeMux()
	mux.HandleFunc("/update/", handler.UpdateHandler(s))

	err := http.ListenAndServe("localhost:8080", mux)
	if err != nil {
		panic(err)
	}
}
