# LCM

![lcm_architecture](./assets/LCM_api.png)

## APIs to test LCM

- Service
  - GET     /api/lcm/service/:service_Name
    - get the information of a service
    - `curl -X GET "localhost:8000/api/lcm/service/service-1"`
  - POST    /api/lcm/service
    - register a service with post json body
    - `curl -X POST -H "Content-Type: application/json" -d '{"service_name":"service-1","module":[],"policy":[{"name":"policy-1","type":"default","trigger":[{"name":"interval-0","type":"Interval","property":{"Interval":1}}],"node":[],"path":[]}]}' localhost:8000/api/lcm/service`
  - PUT     /api/lcm/service
    - reconfigure a service with put json body
    - `curl -X PUT -H "Content-Type: application/json" -d '{"service_name":"service-1","module":[],"policy":[{"name":"policy-1","type":"default","trigger":[{"name":"interval-0","type":"Interval","property":{"Interval":2}}],"node":[],"path":[]}]}' localhost:8000/api/lcm/service`
  - POST    /api/lcm/service/file
    - register a service with a configuration file
    - `curl -X POST -F "file=@config.yaml" localhost:8000/api/lcm/service/file`
  - PUT     /api/lcm/service/file
    - reconfigure a service with a new configuration file
    - `curl -X PUT -F "file=@reconfig.yaml" localhost:8000/api/lcm/service/file`
  - POST    /lcm/service/:service_Name/file
    - upload manifest files
    - `curl -X POST -F "file=@GoServer.yaml" -F "file=@GoServer.zip" localhost:8000/api/lcm/service/service-1/file`
  - DELETE  /api/lcm/service/:service_Name
    - delete a service
    - `curl -X DELETE "localhost:8000/api/lcm/service/service-1"`

- Policy
  - GET /api/lcm/service/:service_Name/policy/:policy_Name
    - get the information of a policy
    - `curl -X GET "localhost:8000/api/lcm/service/service-1/policy/policy-1"`
  - POST /api/lcm/service/:service_Name/policy
    - add a policy to a service
    - `curl -X POST -H "Content-Type: application/json" -d '{"name":"policy-1","type":"default","trigger":[{"name":"interval-0","type":"Interval","property":{"Interval":1}}],"node":[],"path":[]}' localhost:8000/api/lcm/service/service-1/policy`
  - POST /api/lcm/service/policy/deploy
    - deploy/undeploy a service's policies
    - `curl -X POST -H "Content-Type: application/json" -d '{"SvcName":"service-1", "Policies": ["policy-1"]}' localhost:8000/api/lcm/service/policy/deploy`
  - POST /api/lcm/service/policy/undeploy
    - undeploy a service's policies
    - `curl -X POST -H "Content-Type: application/json" -d '{"SvcName":"service-1", "Policies": ["policy-1"]}' localhost:8000/api/lcm/service/policy/undeploy`
  - PUT /api/lcm/service/:service_Name/policy/:policy_Name
    - reconfigure a policy
    - `curl -X PUT -H "Content-Type: application/json" -d '{"name":"policy-1","type":"default","trigger":[{"name":"interval-0","type":"Interval","property":{"Interval":2}}],"node":[],"path":[]}' localhost:8000/api/lcm/service/service-1/policy/policy-1`
  - DELETE /api/lcm/service/:service_Name/policy/:policy_Name
    - delete a policy
    - `curl -X DELETE "localhost:8000/api/lcm/service/service-1/policy/policy-1"`

- DataPacket
  - GET  /api/lcm/service/:service_Name/policy/:policy_Name/datapacket
    - get the information of a node's data packet
    - `curl -X GET "localhost:8000/api/lcm/service/service-1/policy/policy-1/datapacket?node=p1-if-0&port=0"`
  - POST /api/lcm/service/:service_Name/policy/:policy_Name/datapacket
    - set the transformat of a node's data packet
    - `curl -X POST -H "Content-Type: application/json" -d '{"NodeName":"p1-if-0", "Port": 0, "TransFormat": [{"src": "a", "dst": "b"}]}' localhost:8000/api/lcm/service/service-1/policy/policy-1/datapacket`

- Misc
  - POST /api/lcm/webhook
    - send the webhook event to each deployed policies
    - `curl -X POST -H "Content-Type: application/json" -d '{"SvcName":"service-1", "EventType": "default", "Content": {"Value": 10}}' localhost:8000/api/lcm/webhook`