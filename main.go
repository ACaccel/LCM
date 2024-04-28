package main

import (
    "lcm/packages/core/Api"
)

func main() {
    server := api.InitServer()

    server.Serve()
}