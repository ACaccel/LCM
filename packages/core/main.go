package main

import (
    "core/Api"
)

func main() {
    server := api.InitServer()

    server.Serve()
}