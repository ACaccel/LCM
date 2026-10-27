# LCM

![lcm_architecture](./efo/LCM_Enabler/assets/LCM_api.png)

Standalone lifecycle management (LCM) policy engine ported from the IEEE1935 testbed.
Operators define services, each with policies (triggers, function nodes, connections),
deploy them, and drive them with webhook or interval events.

| Component                       | Role                                                                     | Docs                                            |
| ------------------------------- | ------------------------------------------------------------------------ | ----------------------------------------------- |
| `efo/LCM_Enabler/`            | Policy design and management, `/api/lcm` REST API, GUI (`frontend/`) | [LCM Enabler](./efo/LCM_Enabler/README.md)       |
| `efo/Rule_Framework/`         | Runtime engine that executes deployed policies                           | [Rule Framework](./efo/Rule_Framework/README.md) |
| `efo/External_API/apiserver/` | Gin HTTP server exposing the LCM API                                     | -                                               |

The API is unauthenticated; the server binds to localhost only.

## Run

Backend (Go, listens on `127.0.0.1:8000`; run from the repository root, since the upload
and template directories are resolved relative to it):

```sh
go run .
curl localhost:8000/ping
```

GUI (Node.js and npm):

```sh
cd efo/LCM_Enabler/frontend
npm install
npm run dev
```

The full API reference is in [efo/LCM_Enabler/README.md](./efo/LCM_Enabler/README.md);
example configs and scripts are in `efo/LCM_Enabler/examples/`.
