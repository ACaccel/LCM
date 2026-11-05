/**
 * Generated from internal/api/openapi.yaml by `npm run gen:api`. Do not edit.
 */

export interface paths {
    "/health": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** Liveness and database check */
        get: operations["getHealth"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/openapi.yaml": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** This document */
        get: operations["getOpenAPI"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/auth/login": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Start a session
         * @description Sets the `lcm_session` cookie. Failures are rate limited per client address.
         */
        post: operations["login"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/auth/logout": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * End the session
         * @description Session only.
         */
        post: operations["logout"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/auth/me": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * The authenticated caller
         * @description Available to every authenticated caller, including ingest tokens.
         */
        get: operations["getMe"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/tokens": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * List API tokens
         * @description Session only. Secrets are never listed.
         */
        get: operations["listTokens"];
        put?: never;
        /**
         * Create an API token
         * @description Session only. The secret is returned once.
         */
        post: operations["createToken"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/tokens/{id}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post?: never;
        /**
         * Revoke an API token
         * @description Session only.
         */
        delete: operations["revokeToken"];
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/audit": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** Audit log */
        get: operations["listAudit"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/actions/{id}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * Progress and outcome of an action
         * @description Only the most recent 500 actions are remembered, and not across restarts.
         */
        get: operations["getAction"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/check-types": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** Registered check types */
        get: operations["listCheckTypes"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/alerts": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** Firing external alerts */
        get: operations["listAlerts"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/resource-types": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * Registered resource types
         * @description Types, their adapters and the actions they offer. Clients build forms and menus from it instead of repeating server rules.
         */
        get: operations["listResourceTypes"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/resources": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** List managed and retired resources */
        get: operations["listResources"];
        put?: never;
        /** Create a resource */
        post: operations["createResource"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/resource-archives": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * List unmanaged (archived) resources
         * @description Read them with `GET /resources/@{id}`; archives are read only.
         */
        get: operations["listResourceArchives"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/resources/{resource}": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description Name of an active resource, or `@<id>` for any incarnation including archives. */
                resource: components["parameters"]["Resource"];
            };
            cookie?: never;
        };
        /** A resource with its phase, state, health, checks, actions and open operation */
        get: operations["getResource"];
        /**
         * Replace a resource document
         * @description The name in the document must match the URL; type and adapter cannot change. `If-Match` is required.
         */
        put: operations["updateResource"];
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/resources/{resource}/versions": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description Name of an active resource, or `@<id>` for any incarnation including archives. */
                resource: components["parameters"]["Resource"];
            };
            cookie?: never;
        };
        /** Stored versions of a resource document */
        get: operations["listResourceVersions"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/resources/{resource}/versions/{v}": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description Name of an active resource, or `@<id>` for any incarnation including archives. */
                resource: components["parameters"]["Resource"];
                v: components["parameters"]["Version"];
            };
            cookie?: never;
        };
        /** One stored version of a resource document */
        get: operations["getResourceVersion"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/resources/{resource}/restore": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /** Restore an old version as a new one */
        post: operations["restoreResource"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/resources/{resource}/phase": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Retire, resume or unmanage a resource
         * @description `retired` stops observation and control and keeps the records (a running
         *     service is stopped first when its runtime can); `managed` resumes;
         *     `unmanaged` archives the resource and releases its name. Unmanaging requires a
         *     retired resource that no config source owns and, for a service, no running
         *     action and a registered, stopped or failed state. Retiring a service other managed
         *     resources depend on or are controlled through needs `acknowledgeImpact`
         *     (409 `impact_unacknowledged` otherwise; see `GET /resources/{resource}/impact`).
         */
        post: operations["setResourcePhase"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/resources/{resource}/actions/{action}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Run a service lifecycle action
         * @description Starts the action in the background and answers 202 with an action id.
         *     Preconditions that can be checked up front (unknown resource, a type other
         *     than service (409 `unsupported`), wrong state, missing configuration, busy
         *     resource) fail synchronously. A disruptive action (stop, restart, deploy,
         *     rollback, run-job) on a service other managed resources depend on or are
         *     controlled through needs `acknowledgeImpact: true` in the body; without it the
         *     answer is 409 `impact_unacknowledged` with the impact in `error.details.impact`.
         *     With `dryRun=true` nothing is started and the plan, including the impact, is
         *     returned.
         */
        post: operations["runResourceAction"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/resources/{resource}/impact": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * Resources an action on this one reaches
         * @description Managed resources that depend on this one directly or transitively (`dependsOn`,
         *     `runsOn`) and the managed resources controlled through it or through one of its
         *     dependents (`controlPath`). Retired resources are neither observed nor controlled
         *     and do not count. With `action`, `acknowledgementRequired` tells whether that
         *     action needs `acknowledgeImpact`: disruptive and destructive actions and
         *     operations, and retiring a service, when the impact is not empty.
         */
        get: operations["getResourceImpact"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/resource-groups/actions": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Run one service action on a group of services
         * @description Selects the managed services matching every label of the selector and runs the
         *     action on them one after another: the services a service depends on first,
         *     except for `stop`, which stops dependents first. Only service lifecycle actions
         *     run in groups; machine and router operations are refused. Nothing starts unless
         *     every step can run as planned and, when a step's impact needs it,
         *     `acknowledgeImpact` is set. The group stops at the first failed step; the steps
         *     after it are reported as `notRun`. With `dryRun` the ordered plan is returned.
         *     Every step is also a tracked action under `/actions/{id}`.
         */
        post: operations["runResourceGroupAction"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/resource-groups/actions/{id}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * State of a group action
         * @description Group actions are kept in memory; the most recent 100 are available until restart.
         */
        get: operations["getResourceGroupAction"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/resources/{resource}/deployments": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** Deployment history of a service */
        get: operations["listDeployments"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/resources/{resource}/timeline": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** Timeline of one resource */
        get: operations["listResourceTimeline"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/timeline": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** Timeline of all resources */
        get: operations["listTimeline"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/resources/{resource}/checks": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** Current health and checks of a resource */
        get: operations["listChecks"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/resources/{resource}/checks/{check}/series": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * Result history of one check
         * @description Ranges up to six hours inside raw retention use raw results; anything else uses one-minute rollups.
         */
        get: operations["getCheckSeries"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/resources/{resource}/summary": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** Uptime and latency over a window */
        get: operations["getResourceSummary"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/resources/{resource}/observe": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Observe a machine or router now
         * @description Other types answer 409 `unsupported`.
         */
        post: operations["observeResource"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/resources/{resource}/history": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** Stored observations of a machine or router */
        get: operations["listResourceHistory"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/resources/{resource}/events": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** Stored events of a machine or router */
        get: operations["listResourceEvents"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/resources/{resource}/capabilities": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** Prerequisites of the durable operations of a machine or router */
        get: operations["getResourceCapabilities"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/resources/{resource}/maintenance": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description Name of an active resource, or `@<id>` for any incarnation including archives. */
                resource: components["parameters"]["Resource"];
            };
            cookie?: never;
        };
        /** Maintenance state of the local machine */
        get: operations["getResourceMaintenance"];
        put?: never;
        /**
         * Enter maintenance on the local machine
         * @description Holds local writes without a power request; a power request enters maintenance
         *     itself. The request acknowledges partial workload coverage and manual recovery.
         *     Direct administrator requests only; policies are refused.
         */
        post: operations["enterResourceMaintenance"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/resources/{resource}/maintenance/preview": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** Workloads affected by maintenance of the local machine */
        get: operations["previewResourceMaintenance"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/resources/{resource}/maintenance/exit": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Exit maintenance on the local machine
         * @description Needs a reason, no open operation and a verified healthy observation newer than
         *     the entry and any later resolution. Direct administrator requests only; policies
         *     are refused.
         */
        post: operations["exitResourceMaintenance"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/resources/{resource}/operations": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description Name of an active resource, or `@<id>` for any incarnation including archives. */
                resource: components["parameters"]["Resource"];
            };
            cookie?: never;
        };
        /** Durable operations of a machine or router */
        get: operations["listResourceOperations"];
        put?: never;
        /**
         * Submit a durable operation such as a reboot
         * @description One confirmed request: `reason` and `confirm` (the resource's exact name). A
         *     machine power request enters maintenance itself, recording the workload
         *     inventory, unless maintenance is already active; a refused request leaves none
         *     behind. The intent is stored before anything is sent; the outcome is observed
         *     and an unknown one needs `resolve`. The same `idempotencyKey` with the same
         *     action and `If-Match` version returns the stored operation instead of sending
         *     again. Direct administrator requests only; policies are refused. A disruptive or
         *     destructive operation (reboot, graceful-shutdown) on a resource other managed
         *     resources depend on or are controlled through needs `acknowledgeImpact` (409
         *     `impact_unacknowledged` otherwise); send it again when retrying with the same
         *     idempotency key.
         */
        post: operations["submitResourceOperation"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/resources/{resource}/operations/{id}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** One durable operation */
        get: operations["getResourceOperation"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/resources/{resource}/operations/{id}/resolve": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Settle an operation whose outcome is unknown
         * @description Direct administrator requests only; policies are refused.
         */
        post: operations["resolveResourceOperation"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/connections": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * Host-configured connections
         * @description Connections come from the host's connections file (`LCM_CONNECTIONS_FILE`) and
         *     cannot be changed through the API. Addresses, usernames and passwords are never
         *     returned.
         */
        get: operations["listConnections"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/policies": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** List policies */
        get: operations["listPolicies"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/policies/{p}": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                p: components["parameters"]["Policy"];
            };
            cookie?: never;
        };
        /**
         * A policy with its triggers and compile warnings
         * @description Returns the deployed version, or the latest stored one when the policy is not deployed.
         */
        get: operations["getPolicy"];
        /**
         * Validate, store and deploy a policy
         * @description Creates the policy (no `If-Match`, or `*`) or deploys a new version of an
         *     existing one (`If-Match: <current version>`). Modules are resolved from the
         *     module store and embedded in the stored version. A document equal to the
         *     latest version creates no new version but deploys it again.
         */
        put: operations["deployPolicy"];
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/policies/{p}/deployment": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post?: never;
        /**
         * Undeploy a policy
         * @description New events are no longer accepted and queued runs are cancelled; active runs finish. Stored versions are kept.
         */
        delete: operations["undeployPolicy"];
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/policies/{p}/pause": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Pause a deployed policy
         * @description A paused policy rejects new runs; active runs continue.
         */
        post: operations["pausePolicy"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/policies/{p}/resume": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /** Resume a paused policy */
        post: operations["resumePolicy"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/policies/{p}/versions": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** Stored versions of a policy */
        get: operations["listPolicyVersions"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/policies/{p}/versions/{v}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** One stored version of a policy */
        get: operations["getPolicyVersion"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/policies/{p}/state": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** Persistent state of a policy */
        get: operations["listPolicyState"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/policies/{p}/state/{key}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post?: never;
        /** Delete one state key */
        delete: operations["deletePolicyState"];
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/policies/{p}/runs": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Start a run by hand
         * @description Starts a run of the deployed policy at the named trigger node, or at the
         *     policy's only `trigger.manual` node. The request body supplies the trigger
         *     event. A run dropped by the policy's `drop` concurrency mode answers 409 with
         *     the stored run's id in `details.runId`.
         */
        post: operations["startRun"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/validate": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Validate any single document without side effects
         * @description Decodes and checks a Policy, Module or Resource. Problems are a normal result
         *     and answer 200 with `valid: false`; only a request that cannot be read is an
         *     error. Policies are compiled against the module store; resources are checked
         *     against the check types, and relation targets that do not exist yet are
         *     warnings.
         */
        post: operations["validateDocument"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/modules": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** List modules */
        get: operations["listModules"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/modules/{m}": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                m: components["parameters"]["Module"];
            };
            cookie?: never;
        };
        /** A module document */
        get: operations["getModule"];
        /**
         * Create or update a module
         * @description Creates the module (no `If-Match`, or `*`) or saves a new version of an
         *     existing one (`If-Match: <current version>`). Deployed policies keep the
         *     version of the module they were compiled with until they are deployed again.
         */
        put: operations["saveModule"];
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/modules/{m}/versions": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** Stored versions of a module */
        get: operations["listModuleVersions"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/node-types": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * Schemas of all node types
         * @description The source of truth for node palettes, property forms and ports.
         */
        get: operations["listNodeTypes"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/templates": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * List the built-in policy templates
         * @description Templates are Policy documents with `spec.params`; each entry describes
         *     the parameters a form needs. Trigger props cannot be expressions, so the
         *     parameters a trigger uses are written into it when a template is
         *     instantiated; all other nodes keep reading `params.*`.
         */
        get: operations["listTemplates"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/templates/{t}/instantiate": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Create a policy from a template
         * @description Fills the template with `params` (defaults for the rest), compiles the
         *     result and deploys it as a new policy. With `dryRun` nothing is stored and
         *     the concrete document is returned for review. The named resource must exist and be a service.
         */
        post: operations["instantiateTemplate"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/apply": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Apply Resource, Policy and Module documents
         * @description Applies a multi-document YAML stream (documents separated by `---`): modules
         *     first, then resources, then policies, whatever the order in the body. It is not
         *     transactional; every document gets its own result and a failing one does not
         *     stop the others, so the response is 200 even when documents failed. Only a
         *     body without any usable document is a 422.
         *
         *     `dryRun=true` validates and diffs without writing anything. A resource that a
         *     config source owns is not applied (`skipped`) unless `force=true`; a forced
         *     apply leaves the resource owned, so it shows as drifted from the repository.
         */
        post: operations["applyDocuments"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/export": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * Export resources as YAML
         * @description Canonical YAML of existing resources as one multi-document stream, in apply
         *     order (modules, resources, policies) and by name within a kind. Only document
         *     fields are written. Applying the output back changes nothing, so it is the way
         *     to move resources that were created in the web UI under git.
         */
        get: operations["exportDocuments"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/config/sources": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** List config sources */
        get: operations["listConfigSources"];
        put?: never;
        /**
         * Register a config source
         * @description A source is a repository location LCM pulls Resource, Policy and Module documents
         *     from. LCM keeps its own clone under its data directory and never touches the
         *     repository's working copy. The first sync starts at once when the server runs.
         *     The repository URL must not embed a password; use an SSH key or a git
         *     credential helper.
         */
        post: operations["createConfigSource"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/config/sources/{name}": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                name: components["parameters"]["SourceName"];
            };
            cookie?: never;
        };
        /** A config source */
        get: operations["getConfigSource"];
        /**
         * Replace the settings of a config source
         * @description Changing the repository, branch or path forgets the last commit, so the next
         *     sync reads everything again.
         */
        put: operations["replaceConfigSource"];
        post?: never;
        /**
         * Delete a config source
         * @description Removes the source and LCM's clone. The resources it owned keep their content
         *     and become managed through the web UI and API only.
         */
        delete: operations["deleteConfigSource"];
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/config/sources/{name}/sync": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Start a sync of a source now
         * @description Starts a sync in the background and answers 202 with the source at once;
         *     a sync fetches the branch and re-applies its head commit, whatever the
         *     interval. Read `lastSyncAt`, `lastCommit` and `lastError` from the source
         *     (`GET /config/sources/{name}`) once `syncing` is false. A request while a
         *     sync of the same source is running joins it instead of starting another.
         *     Documents are applied one by one under the actor `configsync:<source>`: a
         *     resource that was edited outside the repository (drifted) is never
         *     overwritten, a changed repository document for it is reported as a conflict
         *     (see `GET /config/ownership`, the timeline and the `configsync` alert
         *     signals) instead.
         */
        post: operations["syncConfigSource"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/config/ownership": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * Resources managed by config sources
         * @description Each entry says where the resource's document comes from and whether it
         *     drifted (edited outside the repository), has a conflict (a commit that
         *     could not be applied because of that), or is orphaned (gone from the
         *     repository, kept in LCM). Drift is computed on each call.
         */
        get: operations["listConfigOwnership"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/config/ownership/{kind}/{name}/resolve": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Settle a drifted or conflicting resource
         * @description `use: git` writes the repository document (the skipped newer one if there is a
         *     conflict, otherwise the last applied one) and discards the local edits. For
         *     a document that a source found for an existing, unowned resource with other
         *     content, it applies the file and the source takes ownership.
         *     `use: current` keeps the current content, accepts the skipped document as the
         *     reference and closes the conflict; the resource stays drifted (and, for an
         *     unowned resource, becomes owned) until the repository content equals the
         *     current document, then the drift clears by itself.
         */
        post: operations["resolveConfigOwnership"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/config/ownership/{kind}/{name}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post?: never;
        /**
         * Release a resource from its config source
         * @description The resource keeps its content and becomes managed through the web UI and API only.
         */
        delete: operations["releaseConfigOwnership"];
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/runs": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** List runs */
        get: operations["listRuns"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/runs/{id}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** A run with its per-node decision trace */
        get: operations["getRun"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/runs/{id}/cancel": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /** Cancel a queued or active run */
        post: operations["cancelRun"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/runs/{id}/nodes/{node}/resume": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Resume a suspended node
         * @description The node's `suspendKey` from the run trace must be sent as `key`. A node id inside a module contains slashes and must be percent-encoded.
         */
        post: operations["resumeNode"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/locks": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** Held locks */
        get: operations["listLocks"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/locks/{name}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        post?: never;
        /**
         * Force-release a lock
         * @description Frees the lock whoever holds it; audited. Lock names may contain slashes, which must be percent-encoded.
         */
        delete: operations["forceReleaseLock"];
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/hooks/security/traffic": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Persist typed Nginx traffic measurements without alert semantics
         * @description Accepts sessions, admin and ingest tokens; maximum 16 KiB. Immutable
         *     windows are deduplicated by Machine, Service, category and both window
         *     timestamps. A different payload for an existing window returns 409.
         *     Normal measurements never create incidents or notifications.
         */
        post: operations["ingestSecurityTraffic"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/security/traffic": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** Read bounded typed traffic history for Machine/Service correlation */
        get: operations["listSecurityTraffic"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/hooks/security": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Ingest a sanitized Machine-scoped security event
         * @description Accepts sessions, admin and ingest tokens. Maximum body 16 KiB.
         *     eventId is globally unique and stable across retries. A different body
         *     for an existing id is a conflict. Events older than ten minutes or
         *     explicitly historical are stored without changing live incidents.
         *     Timestamps allow five minutes of future skew and thirty days of history.
         *     No raw command output, authentication material or request bodies belong here.
         */
        post: operations["ingestSecurityEvent"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/security/status": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** Read security ingestion and notification queue health */
        get: operations["getSecurityStatus"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/security/incidents": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** Read bounded security incidents, newest first */
        get: operations["listSecurityIncidents"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/hooks/alerts/{source}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Receive alerts from a monitoring system
         * @description Accepts Alertmanager, Uptime Kuma, Gatus and generic payloads. Allowed for
         *     `admin` and `ingest` tokens. Bodies are limited to 1 MiB.
         */
        post: operations["ingestAlert"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/hooks/github": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Receive a GitHub push event
         * @description Notifies the `trigger.git` nodes that watch the pushed repository and branch.
         *     When `LCM_GITHUB_WEBHOOK_SECRET` names a secret, a valid
         *     `X-Hub-Signature-256` header authenticates the request and no token is
         *     needed; otherwise an `admin` or `ingest` token is required. Other events
         *     are acknowledged and ignored. The name `github` is reserved: a
         *     `trigger.webhook` node cannot be reached under it. Until LCM has
         *     recovered after a start, pushes are refused with 503 and `Retry-After`.
         */
        post: operations["githubPush"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/hooks/{name}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Deliver a webhook to `trigger.webhook` nodes
         * @description Starts a run for every deployed `trigger.webhook` node with this name. A
         *     listener that has an `hmacSecret` requires a valid signature in its header
         *     (default `X-Hub-Signature-256`) and then needs no token; a listener without
         *     one requires an `admin` or `ingest` token. Callers without a token cannot
         *     tell an unknown name from a bad signature. Bodies larger than 128 KiB are
         *     refused. Until LCM has recovered after a start, deliveries are refused
         *     with 503 and `Retry-After`.
         */
        post: operations["deliverWebhook"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/events": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * Live events (server-sent events)
         * @description A `text/event-stream` of what happens in LCM. Each frame has an `id`, an
         *     `event` name (the event type) and `data` holding an `EventEnvelope`. Comment
         *     lines (`: heartbeat`) keep idle connections open every 15 seconds. A client
         *     that cannot keep up receives `event: overflow` and is disconnected; it must
         *     reload its data from the REST API and reconnect. `Last-Event-ID` is not
         *     supported: reload after reconnecting.
         *
         *     Event types and their `data` payloads:
         *
         *     - `signal`: a signal from the bus (check, health, lifecycle, alert, resource.config.*).
         *     - `run`: a run event (`run.queued`, `run.started`, `run.suspended`,
         *       `run.resumed`, `run.finished`).
         *     - `node`: a node event (`node.started`, `node.finished`, `node.retrying`,
         *       `node.suspended`, `node.dead`).
         *     - `timeline`: a stored timeline entry.
         *     - `action`: progress of an action started through the API.
         *     - `resource`: a resource document was created, updated or restored, or its
         *       phase changed (`ResourceChange`).
         *     - `observation`: a committed machine or router event (`ResourceEvent`);
         *       reload durable APIs after reconnect.
         *     - `catalog`: a policy was deployed or undeployed.
         */
        get: operations["streamEvents"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/security/incidents/{id}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * getSecurityIncident
         * @description Admin-only. Ingest tokens cannot read or manage this resource.
         */
        get: operations["getSecurityIncident"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/security/incidents/{id}/handling": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        /**
         * updateSecurityHandling
         * @description Admin-only. Ingest tokens cannot read or manage this resource.
         */
        put: operations["updateSecurityHandling"];
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/security/settings": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * getSecuritySettings
         * @description Admin-only. Ingest tokens cannot read or manage this resource.
         */
        get: operations["getSecuritySettings"];
        /**
         * updateSecuritySettings
         * @description Admin-only. Ingest tokens cannot read or manage this resource.
         */
        put: operations["updateSecuritySettings"];
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/security/test-notification": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * startSecurityNotificationTest
         * @description Admin-only. Ingest tokens cannot read or manage this resource.
         */
        post: operations["startSecurityNotificationTest"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/security/tests/{id}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * getSecurityNotificationTest
         * @description Admin-only. Ingest tokens cannot read or manage this resource.
         */
        get: operations["getSecurityNotificationTest"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/security/traffic/series": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * getSecurityTrafficSeries
         * @description Admin-only. At most seven days and 1000 nominal UTC buckets. Complete observation counters are assigned by window start; gaps remain absent. RPS uses the union of observed intervals, not nominal bucket width. No percentiles or NIC bandwidth.
         */
        get: operations["getSecurityTrafficSeries"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/security/host": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * getSecurityHost
         * @description Admin-only. Ingest tokens cannot read or manage this resource.
         */
        get: operations["getSecurityHost"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/security/host/preview": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * previewSecurityHost
         * @description Admin-only. Ingest tokens cannot read or manage this resource.
         */
        post: operations["previewSecurityHost"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/security/host/apply": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * applySecurityHost
         * @description Admin-only. Ingest tokens cannot read or manage this resource.
         */
        post: operations["applySecurityHost"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/security/host/operations": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * listSecurityHostOperations
         * @description Admin-only. Ingest tokens cannot read or manage this resource.
         */
        get: operations["listSecurityHostOperations"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/security/host/operations/{id}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * getSecurityHostOperation
         * @description Admin-only. Ingest tokens cannot read or manage this resource.
         */
        get: operations["getSecurityHostOperation"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/security/settings/history": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /**
         * Read the last hundred effective notification settings versions
         * @description Admin-only. Ingest tokens cannot read or manage this resource.
         */
        get: operations["getSecuritySettingsHistory"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/heartbeats/{resource}/{check}": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description Name of an active resource, or `@<id>` for any incarnation including archives. */
                resource: components["parameters"]["Resource"];
                check: string;
            };
            cookie?: never;
        };
        /**
         * Report a heartbeat without a body
         * @description For senders that can only issue GET, such as `curl` in a cron line.
         */
        get: operations["getHeartbeat"];
        put?: never;
        /**
         * Report a heartbeat
         * @description The body, if any, is kept as a short message. Allowed for `admin` and `ingest` tokens.
         */
        post: operations["postHeartbeat"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
}
export type webhooks = Record<string, never>;
export interface components {
    schemas: {
        SecuritySettingsHistory: {
            version: number;
            actor: string;
            /** Format: date-time */
            at: string;
            reason: string;
            notificationsEnabled: boolean;
            cooldownSeconds: number;
            /** @enum {string} */
            minimumSeverity: "info" | "warning" | "critical";
        };
        SecurityHandling: {
            version: number;
            acknowledged: boolean;
            note: string;
            /** Format: date-time */
            silenceUntil: string | null;
            /** Format: date-time */
            updatedAt: string | null;
            updatedBy: string;
        };
        SecurityHandlingHistory: {
            id: number;
            action: string;
            reason: string;
            actor: string;
            /** Format: date-time */
            at: string;
            /** Format: date-time */
            silenceUntil: string | null;
        };
        SecurityHandlingUpdate: {
            /** @enum {string} */
            action: "acknowledge" | "note" | "silence" | "unsilence";
            reason: string;
            /** Format: date-time */
            silenceUntil?: string;
        };
        SecurityIncidentDetail: {
            incident: components["schemas"]["SecurityIncident"];
            history: components["schemas"]["SecurityHandlingHistory"][];
        };
        SecuritySettings: {
            version: number;
            notificationsEnabled: boolean;
            cooldownSeconds: number;
            /** @enum {string} */
            minimumSeverity: "info" | "warning" | "critical";
            rootEnabled: boolean;
            notificationConfigured: boolean;
            maxSilenceSeconds: number;
        };
        SecuritySettingsUpdate: {
            notificationsEnabled: boolean;
            cooldownSeconds: number;
            /** @enum {string} */
            minimumSeverity: "info" | "warning" | "critical";
            reason: string;
        };
        SecurityNotificationTest: {
            requestId: string;
            incidentId: string;
            state: string;
            pending: boolean;
        };
        SecurityTestRequest: {
            requestId: string;
            reason: string;
        };
        SecuritySeriesPoint: {
            /** Format: date-time */
            windowStart: string;
            /** Format: date-time */
            windowEnd: string;
            requests: number;
            status429: number;
            status5xx: number;
            dryRunHits: number;
            requestBytes: number;
            responseBytes: number;
            observedWindows: number;
            requestTimeSumMs: number;
            requestTimeMaxMs: number;
            averageMs: number;
            requestsPerSecond: number;
            observedSeconds: number;
        };
        SecuritySeries: {
            items: components["schemas"]["SecuritySeriesPoint"][];
            /** Format: date-time */
            from: string;
            /** Format: date-time */
            to: string;
            bucketSeconds: number;
        };
        SecurityHostProfile: {
            name: string;
            enabled: boolean;
            required: boolean;
        };
        SecurityHostLimit: {
            /** @enum {string} */
            category: "auth" | "api" | "upload" | "socket" | "static" | "other";
            rate: string;
            burst: number;
            connections: number;
        };
        SecurityHostSchedule: {
            hour: number;
            minute: number;
        };
        SecurityHostStatus: {
            /** @description False when this LCM has no security host Machine (LCM_SECURITY_MACHINE_ID); every capability is then disabled and nothing changes until the configuration does, so clients need not poll. */
            configured: boolean;
            available: boolean;
            reason: string;
            machineId: string;
            effectiveDigest: string;
            capabilities: {
                /** @enum {string} */
                action: "aide.check" | "aide.schedule" | "nginx.configure" | "audit.profile" | "aide.profile" | "baseline.request";
                enabled: boolean;
                reason: string;
            }[];
            aide: {
                result: string | null;
                /** Format: date-time */
                lastRunAt: string | null;
                /** Format: date-time */
                nextRunAt: string | null;
                /** Format: date-time */
                baselineCreatedAt: string | null;
                baselineDigest: string | null;
                schedule: components["schemas"]["SecurityHostSchedule"] | null;
                profiles: components["schemas"]["SecurityHostProfile"][];
                targets: string[] | null;
                reportSummary: string | null;
            };
            audit: {
                enabled: number | null;
                lost: number | null;
                backlog: number | null;
                rulesLoaded: boolean | null;
                profiles: components["schemas"]["SecurityHostProfile"][];
            };
            nginx: {
                mode: string | null;
                dryRunOnly: boolean;
                r11Approved: boolean;
                limits: components["schemas"]["SecurityHostLimit"][];
            };
            collector: {
                /** Format: date-time */
                observedAt: string | null;
                /** Format: date-time */
                lastDelivery: string | null;
                queued: number | null;
                dropped: number | null;
                deliveryError: string | null;
                health: {
                    [key: string]: unknown;
                };
            };
        };
        SecurityHostPreviewRequest: {
            requestId: string;
            /** @enum {string} */
            action: "aide.check" | "aide.schedule" | "nginx.configure" | "audit.profile" | "aide.profile" | "baseline.request";
            reason: string;
            expectedDigest: string;
            /** @description Action-specific allowlisted typed settings; no paths, commands, configuration text or credentials. */
            settings: {
                [key: string]: unknown;
            };
        };
        SecurityHostPreview: {
            requestId: string;
            /** @enum {string} */
            action: "aide.check" | "aide.schedule" | "nginx.configure" | "audit.profile" | "aide.profile" | "baseline.request";
            previewHash: string;
            /** Format: date-time */
            expiresAt: string;
            changes: string[];
            requiresLocalApproval: boolean;
        };
        SecurityHostApplyRequest: {
            requestId: string;
            previewHash: string;
            reason: string;
        };
        SecurityHostOperation: {
            seq: number;
            id: string;
            /** @enum {string} */
            action: "aide.check" | "aide.schedule" | "nginx.configure" | "audit.profile" | "aide.profile" | "baseline.request";
            /** @enum {string} */
            state: "prepared" | "running" | "succeeded" | "failed" | "indeterminate" | "awaiting-local-approval";
            reason: string;
            actor: string;
            /** Format: date-time */
            createdAt: string;
            /** Format: date-time */
            updatedAt: string;
            result: string;
            error: string;
            previewHash: string;
            effectiveDigest: string;
        };
        SecurityHostOperations: {
            items: components["schemas"]["SecurityHostOperation"][];
            nextCursor: string | null;
        };
        SecurityTraffic: {
            machineId: string;
            service?: string;
            /** Format: date-time */
            windowStart: string;
            /** Format: date-time */
            windowEnd: string;
            /** @enum {string} */
            category: "auth" | "api" | "upload" | "socket" | "static" | "other";
            requests: number;
            status429: number;
            status5xx: number;
            requestTimeSumMs: number;
            requestTimeMaxMs: number;
            responseBytes: number;
            requestBytes: number;
            dryRunHits: number;
        };
        SecurityEvent: {
            eventId: string;
            machineId: string;
            service?: string;
            /** @enum {string} */
            source: "nginx" | "auditd" | "aide" | "collector";
            fingerprint: string;
            /** @enum {string} */
            status: "firing" | "resolved";
            /** @enum {string} */
            severity: "info" | "warning" | "critical";
            summary: string;
            /** Format: date-time */
            observedAt: string;
            /** @default false */
            historical: boolean;
        };
        SecurityReceipt: {
            accepted: boolean;
            duplicate: boolean;
            incidentId: string;
            historical: boolean;
        };
        SecurityStatus: {
            enabled: boolean;
            notificationsEnabled: boolean;
            machineId: string;
            pending: number;
            failed: number;
            overflow: number;
            expired: number;
            lastError: string;
            /** Format: date-time */
            lastPollAt: string | null;
        };
        SecurityIncident: {
            handling: components["schemas"]["SecurityHandling"];
            seq: number;
            id: string;
            machineId: string;
            service: string;
            /** @enum {string} */
            source: "nginx" | "auditd" | "aide" | "collector" | "machine" | "service";
            fingerprint: string;
            /** @enum {string} */
            state: "firing" | "resolved" | "retired";
            /** @enum {string} */
            severity: "info" | "warning" | "critical";
            summary: string;
            /** Format: date-time */
            openedAt: string;
            /** Format: date-time */
            observedAt: string;
            /** Format: date-time */
            updatedAt: string;
            occurrences: number;
            revision: number;
            /** @enum {string} */
            delivery: "disabled" | "pending" | "sent" | "failed" | "overflow" | "expired" | "filtered";
            lastError: string;
        };
        MachineMaintenancePreview: {
            resourceId: string;
            version: number;
            hash: string;
            partialCoverage: boolean;
            controllerOffline: boolean;
            manualPowerOn: boolean;
            workloads: {
                name: string;
                version: number;
                state: string;
                autostart: string;
            }[];
        };
        MachineCoverage: {
            state: string;
            reason?: string;
        };
        MachineFilesystem: {
            path: string;
            total: number;
            available: number;
            inodes: number;
            freeInodes: number;
        };
        MachineSnapshot: {
            id: number;
            /** Format: date-time */
            observedAt: string;
            bootId: string;
            os: string;
            init: string;
            uptimeSeconds: number;
            cpuTotal: number;
            cpuIdle: number;
            cpuCount: number;
            cpuPercent: number | null;
            load?: number[];
            memoryTotal: number;
            memoryAvailable: number;
            swapTotal: number;
            swapFree: number;
            filesystems: components["schemas"]["MachineFilesystem"][] | null;
            interfaces?: {
                [key: string]: string;
            };
            units?: {
                [key: string]: string;
            };
            pressure?: {
                [key: string]: number;
            };
            coverage: {
                [key: string]: components["schemas"]["MachineCoverage"];
            };
        };
        MachineStatus: {
            snapshot: components["schemas"]["MachineSnapshot"] | null;
            health: string;
            reasons: string[];
            /** @description `reasons` with codes (`health.*`), in the same order. */
            reasonMessages: components["schemas"]["Message"][];
            identity: string;
            journal: components["schemas"]["MachineCoverage"];
            fresh: boolean;
        };
        MachineMaintenance: {
            resourceId: string;
            state: string;
            /** Format: date-time */
            enteredAt: string;
            actor: components["schemas"]["Actor"];
            reason: string;
            acknowledged: boolean;
            /** @description The power action whose request entered maintenance; absent for a manual entry. */
            action?: string;
            preview: components["schemas"]["MachineMaintenancePreview"];
        };
        MachinePowerRequest: {
            controllerId: string;
            operationId: string;
            requestHash: string;
            action: string;
            bootId: string;
            /** Format: date-time */
            deadline: string;
            /** Format: date-time */
            issuedAt: string;
            /** Format: date-time */
            validUntil: string;
        };
        MachineReceipt: {
            state: string;
            /** Format: date-time */
            acceptedAt: string;
            bootId: string;
        };
        ErrorEnvelope: {
            error: {
                /** @description Machine-readable code such as `not_found` or `version_conflict`. */
                code: string;
                message: string;
                details: {
                    [key: string]: unknown;
                };
            };
        };
        ValidationErrorEnvelope: {
            error: {
                /** @enum {string} */
                code: "validation_failed";
                message: string;
                details: {
                    issues: components["schemas"]["Issue"][];
                };
            };
        };
        /**
         * @description A sentence the server produces for people, with a stable code and
         *     parameters so that clients can translate it. `message` is the English
         *     text; clients show it for a code they do not know. Free-form text has
         *     an empty code.
         */
        Message: {
            /** @description Such as `health.stale` or `step.lock`; empty for free-form text. */
            code: string;
            params: {
                [key: string]: string;
            };
            message: string;
        };
        Issue: {
            /** @description Location in the document, such as `spec.nodes[3].next.true[0]`. */
            path: string;
            message: string;
        };
        Actor: {
            /** @enum {string} */
            kind: "user" | "token" | "ai" | "system";
            id: string;
            name: string;
        };
        ServerHealth: {
            status: string;
            version: string;
        };
        LoginRequest: {
            username: string;
            /** Format: password */
            password: string;
        };
        LoginResponse: {
            actor: components["schemas"]["Actor"];
            /** Format: date-time */
            expiresAt: string;
        };
        /** @enum {string} */
        TokenScope: "admin" | "ingest";
        TokenCreateRequest: {
            name: string;
            /** @default admin */
            scope: components["schemas"]["TokenScope"];
        };
        Token: {
            /** Format: int64 */
            id: number;
            name: string;
            scope: components["schemas"]["TokenScope"];
            /** Format: date-time */
            createdAt: string;
            /** Format: date-time */
            lastUsedAt: string | null;
            /** Format: date-time */
            revokedAt: string | null;
        };
        TokenCreated: components["schemas"]["Token"] & {
            /** @description The secret. It is shown only here. */
            token: string;
        };
        AuditEntry: {
            /** Format: int64 */
            id: number;
            /** Format: date-time */
            time: string;
            actor: components["schemas"]["Actor"];
            action: string;
            target: string;
            details: {
                [key: string]: unknown;
            };
            /** @enum {string} */
            outcome: "ok" | "denied" | "error";
        };
        /**
         * @description Go duration such as `30s` or `5m`.
         * @example 30s
         */
        Duration: string;
        Labels: {
            [key: string]: string;
        };
        Metadata: {
            name: string;
            labels?: components["schemas"]["Labels"];
            /**
             * @description Free-form data for tools around LCM; the engine ignores it. Keys follow the
             *     label key syntax, and keys plus values may total 64 KiB. The web console keeps
             *     the policy graph layout in `lcm.dev/layout` (JSON) and reads an external
             *     dashboard link of a service from `lcm.dev/dashboard-url`.
             */
            annotations?: components["schemas"]["Labels"];
        };
        DocMeta: {
            version: number;
            /** Format: date-time */
            updatedAt: string;
            updatedBy: components["schemas"]["Actor"];
        };
        Command: {
            /** @description Run with `sh -c`. */
            command: string;
            timeout?: components["schemas"]["Duration"];
        };
        Check: {
            name: string;
            /** @description See `GET /check-types`. */
            type: string;
            interval?: components["schemas"]["Duration"];
            timeout?: components["schemas"]["Duration"];
            /** @description Default true. */
            critical?: boolean;
            threshold?: number;
            props?: {
                [key: string]: unknown;
            };
        };
        PolicyNode: {
            id: string;
            /** @description See `GET /node-types`. */
            type: string;
            props?: {
                [key: string]: unknown;
            };
            /** @description Output port to the ids of the nodes it feeds. */
            next?: {
                [key: string]: string[];
            };
            timeout?: components["schemas"]["Duration"];
            retry?: {
                max: number;
                backoff?: components["schemas"]["Duration"];
                maxBackoff?: components["schemas"]["Duration"];
            };
            /** @enum {string} */
            joinMode?: "all" | "any";
        };
        /** @description A `lcm/v2` Policy. */
        PolicyDocument: {
            /** @enum {string} */
            apiVersion: "lcm/v2";
            /** @enum {string} */
            kind: "Policy";
            metadata: components["schemas"]["Metadata"];
            spec: {
                concurrency?: {
                    /** @enum {string} */
                    mode?: "queue" | "drop" | "replace" | "parallel";
                    limit?: number;
                };
                timeout?: components["schemas"]["Duration"];
                onInterrupt?: string;
                params?: {
                    [key: string]: unknown;
                };
                nodes: components["schemas"]["PolicyNode"][];
            };
        };
        /** @description A `lcm/v2` Module. */
        ModuleDocument: {
            /** @enum {string} */
            apiVersion: "lcm/v2";
            /** @enum {string} */
            kind: "Module";
            metadata: components["schemas"]["Metadata"];
            spec: {
                params?: components["schemas"]["ModuleParam"][];
                nodes: components["schemas"]["PolicyNode"][];
            };
        };
        ModuleParam: {
            name: string;
            default?: unknown;
            description?: string;
        };
        /** @enum {string} */
        ServiceState: "registered" | "deploying" | "running" | "updating" | "rolling_back" | "stopped" | "failed";
        /** @enum {string} */
        Health: "unknown" | "healthy" | "degraded" | "down";
        /** @enum {string} */
        ActionName: "deploy" | "rollback" | "start" | "stop" | "restart" | "backup" | "run-job";
        ActionRequest: {
            /** @description Commit to deploy (7 to 64 hex digits); deploy only. The head of the configured branch when omitted. */
            version?: string;
            /** @description Name of the job; run-job only, and required there. */
            job?: string;
            /** @description Accept the impact of a disruptive action; see the dry run's `impact`. */
            acknowledgeImpact?: boolean;
        };
        ActionSteps: {
            resource: string;
            action: components["schemas"]["ActionName"];
            from: components["schemas"]["ServiceState"];
            to: components["schemas"]["ServiceState"];
            steps: string[];
            /** @description `steps` with codes (`step.*`), in the same order. */
            stepMessages: components["schemas"]["Message"][];
        };
        ActionPlan: components["schemas"]["ActionSteps"] & {
            impact: components["schemas"]["ResourceImpact"];
        };
        ImpactEntry: {
            name: string;
            type: components["schemas"]["ResourceTypeName"];
            /**
             * @description The relation of this resource to `via`.
             * @enum {string}
             */
            relation: "dependsOn" | "runsOn" | "controlPath";
            /** @description The resource the relation targets. */
            via: string;
            /** @description 1 when the relation targets the acted-on resource. */
            depth: number;
        };
        ResourceImpact: {
            resource: string;
            /** @description Managed resources that depend on the resource, nearest first. */
            dependents: components["schemas"]["ImpactEntry"][];
            /** @description Managed resources LCM reaches through the resource or one of its dependents. */
            controlDependents: components["schemas"]["ImpactEntry"][];
            /** @description Relation targets of the resource that do not exist. */
            unresolved: string[];
            /** @description The action the guard was evaluated for. */
            action?: string;
            /** @description The action needs `acknowledgeImpact`; always false without an action. */
            acknowledgementRequired: boolean;
        };
        ResourceGroupActionRequest: {
            selector: {
                /**
                 * @description Only services run group actions.
                 * @enum {string}
                 */
                type?: "service";
                labels: {
                    [key: string]: string;
                };
            };
            action: components["schemas"]["ActionName"];
            params?: {
                /** @description Commit to deploy; deploy only. */
                version?: string;
                /** @description Job to run; run-job only */
                job?: string;
            };
            /** @default false */
            dryRun: boolean;
            /** @default false */
            acknowledgeImpact: boolean;
        };
        ResourceGroupSelector: {
            /** @enum {string} */
            type: "service";
            labels: {
                [key: string]: string;
            };
        };
        ResourceGroupPlan: {
            action: components["schemas"]["ActionName"];
            selector: components["schemas"]["ResourceGroupSelector"];
            /** @description In execution order. */
            steps: {
                resource: string;
                plan: components["schemas"]["ActionSteps"] | null;
                /** @description Why the step could not run now; empty when it can. */
                error: string;
                impact: components["schemas"]["ResourceImpact"];
            }[];
            /** @description Some step needs `acknowledgeImpact`. */
            acknowledgementRequired: boolean;
        };
        ResourceGroupAction: {
            id: string;
            action: components["schemas"]["ActionName"];
            /** @enum {string} */
            status: "running" | "succeeded" | "failed";
            actor: components["schemas"]["Actor"];
            selector: components["schemas"]["ResourceGroupSelector"];
            version?: string;
            job?: string;
            /** Format: date-time */
            startedAt: string;
            /** Format: date-time */
            finishedAt: string | null;
            steps: {
                resource: string;
                /** @enum {string} */
                status: "pending" | "running" | "succeeded" | "failed" | "notRun";
                /** @description The step's tracked action; empty until it starts. */
                actionId: string;
                result: components["schemas"]["ActionResult"] | null;
                error: string;
            }[];
        };
        ActionResult: {
            resource: string;
            action: components["schemas"]["ActionName"];
            from: components["schemas"]["ServiceState"];
            to: components["schemas"]["ServiceState"];
            /** @description The deployed commit. */
            version?: string;
            previousVersion?: string;
            /** Format: int64 */
            deploymentId?: number;
            /** Format: int64 */
            durationMs: number;
            /** @description Tail of a backup or job command's output. */
            output?: string;
            replayed?: boolean;
        };
        Action: {
            id: string;
            resource: string;
            action: components["schemas"]["ActionName"];
            /** @enum {string} */
            status: "running" | "succeeded" | "failed";
            actor: components["schemas"]["Actor"];
            version?: string;
            job?: string;
            /** Format: date-time */
            startedAt: string;
            /** Format: date-time */
            finishedAt: string | null;
            result: components["schemas"]["ActionResult"] | null;
            error: string;
        };
        Deployment: {
            /** Format: int64 */
            id: number;
            resource: string;
            /** @enum {string} */
            action: "deploy" | "rollback";
            version: string;
            previousVersion: string;
            /** Format: date-time */
            startedAt: string;
            /** Format: date-time */
            finishedAt: string | null;
            /** @enum {string} */
            outcome: "running" | "succeeded" | "failed" | "interrupted";
            actor: components["schemas"]["Actor"];
            runId: string;
            error: string;
            logExcerpt: string;
        };
        DeploymentList: {
            items: components["schemas"]["Deployment"][];
            nextCursor: string | null;
        };
        TimelineEntry: {
            /**
             * Format: int64
             * @description Row id; 0 in `timeline` events for entries whose id is not known.
             */
            id: number;
            resource: string;
            /** Format: date-time */
            time: string;
            /** @description For example `config.changed`, `lifecycle.changed`, `deployment`, `action`, `backup`, `job`, `check.changed`, `health.changed`, `alert`. */
            kind: string;
            summary: string;
            /** @description `summary` with a code (`timeline.*`) derived from the kind and data; entries whose data predates a code keep an empty code. */
            summaryMessage: components["schemas"]["Message"];
            data: {
                [key: string]: unknown;
            };
            /** @description Run id, deployment id or signal id the entry relates to. */
            ref: string;
        };
        TimelineList: {
            items: components["schemas"]["TimelineEntry"][];
            nextCursor: string | null;
        };
        CheckStatus: {
            name: string;
            type: string;
            critical: boolean;
            /** Format: int64 */
            intervalMs: number;
            /** @enum {string} */
            status: "unknown" | "up" | "down";
            /** @description The stored status is older than twice the interval; `status` reads unknown until a fresh result. */
            stale: boolean;
            /** Format: date-time */
            since: string | null;
            message: string;
            /** Format: date-time */
            lastResult: string | null;
            /** Format: int64 */
            lastLatencyMs: number;
            consecutiveFailures: number;
        };
        Checks: {
            resource: string;
            health: components["schemas"]["Health"];
            /** Format: date-time */
            since: string | null;
            checks: components["schemas"]["CheckStatus"][];
        };
        SeriesPoint: {
            /** Format: date-time */
            time: string;
            /** Format: int64 */
            count: number;
            /** Format: int64 */
            failures: number;
            avgLatencyMs: number;
            minLatencyMs: number;
            maxLatencyMs: number;
        };
        Series: {
            resource: string;
            check: string;
            /** Format: date-time */
            from: string;
            /** Format: date-time */
            to: string;
            /** @enum {string} */
            source: "raw" | "rollup";
            /**
             * Format: int64
             * @description Bucket width; 0 when every point is one raw result.
             */
            stepMs: number;
            points: components["schemas"]["SeriesPoint"][];
        };
        SummaryStats: {
            /** Format: int64 */
            count: number;
            /** Format: int64 */
            failures: number;
            /** @description Share of successful runs, 0 to 1; 0 with `count` 0 means no data. */
            uptime: number;
            avgLatencyMs: number;
            minLatencyMs: number;
            maxLatencyMs: number;
        };
        ResourceSummaryStats: components["schemas"]["SummaryStats"] & {
            resource: string;
            /** @enum {string} */
            window: "24h" | "7d" | "30d";
            checks: {
                [key: string]: components["schemas"]["SummaryStats"];
            };
        };
        CheckType: {
            type: string;
            description: string;
        };
        Alert: {
            source: string;
            fingerprint: string;
            resource?: string;
            name: string;
            /** @enum {string} */
            severity: "info" | "warning" | "critical";
            labels?: components["schemas"]["Labels"];
            message?: string;
            /** Format: date-time */
            startedAt: string;
            /** Format: date-time */
            updatedAt: string;
            /** Format: date-time */
            resolvedAt?: string;
        };
        AlertList: {
            items: components["schemas"]["Alert"][];
            nextCursor: string | null;
        };
        Trigger: {
            id: string;
            type: string;
            props: {
                [key: string]: unknown;
            };
        };
        LastRun: {
            id: string;
            status: components["schemas"]["RunStatus"];
            mode: components["schemas"]["RunMode"];
            /** Format: date-time */
            createdAt: string;
        };
        PolicySummary: {
            name: string;
            labels: components["schemas"]["Labels"];
            latestVersion: number;
            deployedVersion: number | null;
            deployed: boolean;
            paused: boolean;
            /** Format: date-time */
            deployedAt: string | null;
            /** Format: date-time */
            updatedAt: string;
            triggers: components["schemas"]["Trigger"][];
            /** @description Nodes of the deployed version that act on a resource; empty when not deployed. */
            targets: components["schemas"]["PolicyTarget"][];
            lastRun: components["schemas"]["LastRun"] | null;
            managedBy: components["schemas"]["ManagedBy"];
        };
        PolicyTarget: {
            /** @description Scoped node id such as deploy or mod/restart. */
            node: string;
            type: string;
            /** @description The resource the node acts on; null when the policy decides it while running (for example from the trigger event). */
            resource: string | null;
        };
        PolicyList: {
            items: components["schemas"]["PolicySummary"][];
            nextCursor: string | null;
        };
        PolicyDoc: components["schemas"]["DocMeta"] & {
            document: components["schemas"]["PolicyDocument"];
        };
        PolicyDetail: components["schemas"]["PolicyDoc"] & {
            name: string;
            labels: components["schemas"]["Labels"];
            latestVersion: number;
            deployedVersion: number | null;
            deployed: boolean;
            paused: boolean;
            /** Format: date-time */
            deployedAt: string | null;
            triggers: components["schemas"]["Trigger"][];
            lastRun: components["schemas"]["LastRun"] | null;
            managedBy: components["schemas"]["ManagedBy"];
            /** @description Non-fatal compiler findings such as unreachable nodes. */
            warnings: components["schemas"]["Issue"][];
        };
        PauseState: {
            name: string;
            paused: boolean;
        };
        DocVersionInfo: {
            version: number;
            /** Format: date-time */
            createdAt: string;
            createdBy: components["schemas"]["Actor"];
        };
        DocVersionList: {
            items: components["schemas"]["DocVersionInfo"][];
            nextCursor: string | null;
        };
        StateEntry: {
            key: string;
            value: unknown;
            /** Format: int64 */
            version: number;
            /** Format: date-time */
            updatedAt: string;
        };
        StateList: {
            items: components["schemas"]["StateEntry"][];
            nextCursor: string | null;
        };
        /**
         * @description Set when a config source manages the resource; null when only the web UI and
         *     API do. `drifted`: edited outside the repository. `conflict`: a commit was
         *     skipped because of that.
         */
        ManagedBy: {
            source: string;
            /** @description File in the repository. */
            path: string;
            commit: string;
            drifted: boolean;
            conflict: boolean;
        } | null;
        ApplyResult: {
            /** @description Position of the document in the request body. */
            index: number;
            /** @description Empty when the document does not say. */
            kind: string;
            name: string;
            /** @enum {string} */
            action: "create" | "update" | "unchanged" | "skipped" | "failed";
            /** @description Resource version after the apply; 0 when nothing was written or known. */
            version: number;
            /** @description Unified diff of the canonical YAML, current to new. */
            diff: string;
            reason: string;
            issues: components["schemas"]["Issue"][];
            warnings: components["schemas"]["Issue"][];
            /** @description The repository changed a resource that was edited locally. */
            conflict: boolean;
            drifted: boolean;
            /** @description File of a document that came from a config source; `-` for a request body. */
            path: string;
        };
        ApplyResponse: {
            dryRun: boolean;
            items: components["schemas"]["ApplyResult"][];
        };
        ConfigSourceRequest: {
            /** @description 1-63 characters of a-z, 0-9, `.`, `_`, `-`. Required on create; on replace it must match the URL. */
            name?: string;
            /** @description Repository URL or local path. */
            repo: string;
            /** @default main */
            branch: string;
            /**
             * @description Directory (or file) in the repository; every `*.yaml` and `*.yml` below it is read, except `*.test.yaml`.
             * @default .
             */
            path: string;
            /**
             * @description Go duration between syncs, 10s to 24h.
             * @default 1m
             */
            interval: string;
            /** @default true */
            enabled: boolean;
        };
        ConfigSource: {
            name: string;
            repo: string;
            branch: string;
            path: string;
            interval: string;
            enabled: boolean;
            /** @description Commit the last sync read; empty before the first. */
            lastCommit: string;
            /** Format: date-time */
            lastSyncAt: string | null;
            /** @description What went wrong in the last sync, empty if nothing. */
            lastError: string;
            /** @description A sync of this source is running now. */
            syncing: boolean;
            /** Format: date-time */
            createdAt: string;
            /** Format: date-time */
            updatedAt: string;
        };
        ConfigSourceList: {
            items: components["schemas"]["ConfigSource"][];
            nextCursor: string | null;
        };
        Ownership: {
            /** @enum {string} */
            kind: "Resource" | "Policy" | "Module";
            name: string;
            source: string;
            path: string;
            commit: string;
            appliedVersion: number;
            /** Format: date-time */
            appliedAt: string;
            drifted: boolean;
            /** @description The managed resource was deleted. */
            missing: boolean;
            conflict: boolean;
            conflictCommit: string;
            /** @description The operator chose to keep the current content. */
            acknowledged: boolean;
            orphaned: boolean;
        };
        OwnershipList: {
            items: components["schemas"]["Ownership"][];
            nextCursor: string | null;
        };
        ValidationResult: {
            /** @description Empty when the document does not say. */
            kind: string;
            name: string;
            valid: boolean;
            issues: components["schemas"]["Issue"][];
            warnings: components["schemas"]["Issue"][];
        };
        /**
         * @description Titles, descriptions and parameter descriptions are English; clients
         *     translate them by template `id` and parameter `name`.
         */
        Template: {
            id: string;
            title: string;
            description: string;
            /** @description Name of the parameter that selects the target resource, a service. */
            resourceParam: string;
            params: components["schemas"]["TemplateParam"][];
        };
        TemplateParam: {
            name: string;
            /**
             * @description `secret` is the name of a secret, `service` the name of a service
             *     resource, `duration` Go syntax such as `5m`, `strings` a list of
             *     strings, `json` any JSON value and `windows` a list of
             *     `{days, start, end}` deploy windows (days `mon`..`sun`, optional;
             *     start and end `HH:MM`, end up to `24:00`).
             * @enum {string}
             */
            type: "string" | "boolean" | "duration" | "secret" | "service" | "strings" | "json" | "windows";
            description: string;
            required: boolean;
            /** @description Name of a boolean parameter that makes this one required while it is true. */
            requiredIf?: string;
            /** @description Absent for required parameters. */
            default?: unknown;
            /** @description The allowed values of a string parameter. */
            enum?: string[];
            /**
             * @description Path in the `spec.config` of the resource that the resource
             *     parameter selects, such as `source.branch`; a form may fill the
             *     parameter from it until the user edits it.
             */
            prefill?: string;
        };
        TemplateInstance: {
            template: string;
            name: string;
            /** @description The concrete policy document. */
            yaml: string;
            /** @description Null for a dry run. */
            version: number | null;
            deployed: boolean;
            warnings: components["schemas"]["Issue"][];
        };
        ModuleSummary: {
            name: string;
            labels: components["schemas"]["Labels"];
            params: components["schemas"]["ModuleParam"][];
            version: number;
            /** Format: date-time */
            updatedAt: string;
            updatedBy: components["schemas"]["Actor"];
        };
        ModuleList: {
            items: components["schemas"]["ModuleSummary"][];
            nextCursor: string | null;
        };
        ModuleDoc: components["schemas"]["DocMeta"] & {
            document: components["schemas"]["ModuleDocument"];
        };
        NodeProp: {
            name: string;
            /** @enum {string} */
            type: "string" | "integer" | "number" | "boolean" | "duration" | "array" | "object" | "any";
            required?: boolean;
            /** @description The value is a bare expression. */
            expr?: boolean;
            enum?: string[];
            default?: unknown;
            description?: string;
            items?: components["schemas"]["NodeProp"];
            props?: components["schemas"]["NodeProp"][];
        };
        NodeType: {
            type: string;
            category: string;
            description?: string;
            /** @enum {string} */
            effect: "none" | "read" | "write";
            /** @description Output ports; `error` is implicit. */
            ports: string[];
            /** @description Ports depend on the props; `ports` lists only the fixed ones. */
            dynamicPorts?: boolean;
            props: components["schemas"]["NodeProp"][];
            /** @description The props are a free-form object. */
            additionalProps?: boolean;
        };
        /** @enum {string} */
        RunStatus: "queued" | "running" | "suspended" | "succeeded" | "failed" | "cancelled" | "interrupted" | "dropped";
        /** @enum {string} */
        RunMode: "live" | "dry";
        /** @enum {string} */
        NodeStatus: "pending" | "ready" | "running" | "retrying" | "suspended" | "completed" | "failed" | "dead";
        StartRunRequest: {
            /** @description Trigger node id; the only `trigger.manual` node when omitted. */
            trigger?: string;
            /** @description Becomes `trigger` in the run's expressions. */
            event?: unknown;
            /** @default live */
            mode: components["schemas"]["RunMode"];
        };
        RunStarted: {
            runId: string;
            policy: string;
            trigger: string;
            mode: components["schemas"]["RunMode"];
        };
        RunRef: {
            runId: string;
        };
        ResumeRequest: {
            /** @description The node's `suspendKey`. */
            key: string;
            /** @description Handed to the node as the resume payload. */
            payload?: unknown;
        };
        RunSummary: {
            id: string;
            policy: string;
            version: number;
            status: components["schemas"]["RunStatus"];
            mode: components["schemas"]["RunMode"];
            triggerNode: string;
            error?: string;
            /** Format: date-time */
            createdAt: string;
            /** Format: date-time */
            startedAt?: string;
            /** Format: date-time */
            finishedAt?: string;
        };
        RunList: {
            items: components["schemas"]["RunSummary"][];
            nextCursor: string | null;
        };
        RunNode: {
            /** @description Scoped id such as `outer/inner/check` for module nodes. */
            id: string;
            type: string;
            status: components["schemas"]["NodeStatus"];
            /** @description The output port the node took. */
            port?: string;
            /** @description Resolved props. */
            props?: unknown;
            output?: unknown;
            attempts?: number;
            error?: string;
            /** Format: date-time */
            startedAt?: string;
            /** Format: date-time */
            finishedAt?: string;
            suspendKey?: string;
            /** Format: date-time */
            resumeAt?: string;
        };
        Run: components["schemas"]["RunSummary"] & {
            /** @description The trigger event. */
            event: unknown;
            /** @description In topological order. */
            nodes: components["schemas"]["RunNode"][];
        };
        LockInfo: {
            name: string;
            owner: string;
            /** Format: date-time */
            acquiredAt: string;
            /** Format: date-time */
            expiresAt: string;
        };
        LockList: {
            items: components["schemas"]["LockInfo"][];
            nextCursor: string | null;
        };
        GitHubAck: {
            event: string;
            /** @description True for events that start nothing (pings, tags, deleted branches). */
            ignored?: boolean;
            repository?: string;
            branch?: string;
            commit?: string;
            /** @description Number of `trigger.git` nodes notified. */
            matched?: number;
            /** @description Number of config sources that will sync because of this push. */
            sources?: number;
        };
        /** @enum {string} */
        EventType: "signal" | "run" | "node" | "timeline" | "action" | "resource" | "observation" | "catalog";
        /** @description Payload of `run` and `node` events, and of policy deployments in `catalog` events. */
        EngineEvent: {
            /** @enum {string} */
            kind: "policy.deployed" | "policy.undeployed" | "run.queued" | "run.started" | "run.suspended" | "run.resumed" | "run.finished" | "node.started" | "node.finished" | "node.retrying" | "node.suspended" | "node.dead";
            /** Format: date-time */
            time: string;
            policy: string;
            version?: number;
            runId?: string;
            mode?: components["schemas"]["RunMode"];
            /** @description Run status for run events */
            status?: string;
            nodeId?: string;
            nodeType?: string;
            port?: string;
            attempt?: number;
            error?: string;
            /** Format: date-time */
            retryAt?: string;
        };
        /** @description Payload of `signal` events. */
        Signal: {
            id: string;
            /** @description check.changed, health.changed, lifecycle.changed, alert, or resource.config.conflict / resource.config.invalid from config sync. */
            kind: string;
            /** Format: date-time */
            time: string;
            resource?: string;
            resourceType?: components["schemas"]["ResourceTypeName"];
            source: string;
            severity?: string;
            labels?: components["schemas"]["Labels"];
            data?: {
                [key: string]: unknown;
            };
        };
        /** @description The `data` of every server-sent event. */
        EventEnvelope: {
            /**
             * Format: int64
             * @description Increases with every event.
             */
            id: number;
            type: components["schemas"]["EventType"];
            /** Format: date-time */
            time: string;
            /** @description Name of the resource the event is about. */
            resource?: string;
            /** @description Immutable resource ID, for resource changes and observations. */
            resourceId?: string;
            policy?: string;
            run?: string;
            /** @description Depends on `type`. */
            data: components["schemas"]["Signal"] | components["schemas"]["EngineEvent"] | components["schemas"]["TimelineEntry"] | components["schemas"]["Action"] | components["schemas"]["ResourceChange"] | components["schemas"]["ResourceEvent"];
        };
        /** @enum {string} */
        ResourceTypeName: "service" | "machine" | "router";
        /**
         * @description `managed` is observed and controlled; `retired` keeps records without observation or control and can be resumed; `unmanaged` is archived and has released its name.
         * @enum {string}
         */
        ResourcePhase: "managed" | "retired" | "unmanaged";
        ResourceAction: {
            /** @description Action, operation or phase change, such as `restart`, `reboot` or `retire`. */
            name: string;
            available: boolean;
            /** @description Why the action is unavailable now; empty when available. Services use notConfigured, unsupported, invalidState, notManaged or busy. */
            reason: string;
            /** @enum {string} */
            risk: "read" | "reversible" | "disruptive" | "destructive";
            /** @description Whether policies may run it. */
            policy: boolean;
            /**
             * @description `action` runs through `POST /resources/{resource}/actions/{action}` (services) or its own route (observe, maintenance); `operation` through `POST /resources/{resource}/operations`; `phase` through `POST /resources/{resource}/phase`.
             * @enum {string}
             */
            kind: "action" | "operation" | "phase";
        };
        ResourceType: {
            type: components["schemas"]["ResourceTypeName"];
            adapters: string[];
            /** @description The adapter reaches the resource through a connection named in `spec.connection` and defined in host configuration. */
            needsConnection: boolean;
            description: string;
            /** @description Everything the type offers; `available` and `reason` are only meaningful on a resource. */
            actions: components["schemas"]["ResourceAction"][];
        };
        Relation: {
            /** @enum {string} */
            type: "dependsOn" | "runsOn" | "controlPath";
            /** @description Name of another resource. */
            target: string;
        };
        /** @description A `lcm/v2` Resource. */
        ResourceDocument: {
            /** @enum {string} */
            apiVersion: "lcm/v2";
            /** @enum {string} */
            kind: "Resource";
            metadata: components["schemas"]["Metadata"];
            spec: {
                type: components["schemas"]["ResourceTypeName"];
                /** @description service: systemd, docker, compose, command or external; machine: local; router: xiaomi. See `GET /resource-types`. */
                adapter: string;
                /** @description Name of a host-configured connection; only for adapters that need one. */
                connection?: string;
                /**
                 * @description Typed by `type` and `adapter` and validated strictly by the server (unknown
                 *     fields are refused). service: the runtime fields valid for the adapter
                 *     (`unit`, `scope`, `container`, `project`, `file`, `start`, `stop`,
                 *     `restart`, `status`) plus `source`, `backup` and `jobs`. machine:
                 *     `interval`, `staleAfter`, `filesystems`, `interfaces`, `units`, `journal`,
                 *     `thresholds`. router: `interval`, `staleAfter`, `thresholds`
                 *     (`cpuPercent`, `memoryPercent`, `consecutive`, `hysteresis`).
                 */
                config?: {
                    [key: string]: unknown;
                };
                checks?: components["schemas"]["Check"][];
                relations?: components["schemas"]["Relation"][];
            };
        };
        ResourceSummary: {
            /** @description Immutable ID; `@<id>` addresses any incarnation */
            id: string;
            name: string;
            type: components["schemas"]["ResourceTypeName"];
            adapter: string;
            phase: components["schemas"]["ResourcePhase"];
            labels: components["schemas"]["Labels"];
            /** @description service: registered, deploying, running, updating, rolling_back, stopped or failed; machine and router: online, offline or unknown. Empty when not known. */
            state: string;
            /** Format: date-time */
            stateSince: string | null;
            health: components["schemas"]["Health"];
            /** @description Version of the configuration document. */
            version: number;
            /** Format: date-time */
            updatedAt: string;
            /** @description Commit of a service's last successful deployment; empty if none and for other types. */
            deployedVersion: string;
            managedBy: components["schemas"]["ManagedBy"];
        };
        ResourceList: {
            items: components["schemas"]["ResourceSummary"][];
            nextCursor: string | null;
        };
        ResourceHealth: {
            status: components["schemas"]["Health"];
            /** Format: date-time */
            since: string | null;
            /** @description The type's own findings (thresholds, reachability) followed by failing checks. */
            reasons: string[];
            /** @description `reasons` with codes (`health.*`), in the same order. */
            reasonMessages: components["schemas"]["Message"][];
        };
        ResourceDetail: {
            id: string;
            name: string;
            type: components["schemas"]["ResourceTypeName"];
            adapter: string;
            /** @description Empty when the adapter needs none. */
            connection: string;
            phase: components["schemas"]["ResourcePhase"];
            labels: components["schemas"]["Labels"];
            document: components["schemas"]["ResourceDocument"];
            version: number;
            /** Format: date-time */
            createdAt: string;
            /** Format: date-time */
            updatedAt: string;
            updatedBy: components["schemas"]["Actor"];
            /** @description See `ResourceSummary.state`. */
            state: string;
            /** Format: date-time */
            stateSince: string | null;
            health: components["schemas"]["ResourceHealth"];
            checks: components["schemas"]["CheckStatus"][];
            alerts: components["schemas"]["Alert"][];
            /** @description What can be done now. A service rollback may still find no target; the dry-run is definitive. */
            actions: components["schemas"]["ResourceAction"][];
            /** @description The durable operation whose outcome is still open, if any. */
            operation: components["schemas"]["ResourceOperation"] | null;
            managedBy: components["schemas"]["ManagedBy"];
            /** @description Relation targets of the document that name no resource yet, sorted; such a relation counts once the target exists. Empty for archives. */
            unresolvedRelations: string[];
            /** @description Services only. */
            service?: {
                deployedVersion: string;
                lastDeployment: components["schemas"]["Deployment"] | null;
            };
            /** @description Machines only. */
            machine?: {
                status: components["schemas"]["MachineStatus"] | null;
            };
            /** @description Routers only. */
            router?: {
                status: components["schemas"]["RouterStatus"] | null;
            };
        };
        ResourcePhaseRequest: {
            phase: components["schemas"]["ResourcePhase"];
            /** @description The current document version; `If-Match` may carry it instead. */
            version?: number;
            reason?: string;
            /** @description Accept the impact of retiring a service (it is stopped first). */
            acknowledgeImpact?: boolean;
        };
        ResourceVersion: components["schemas"]["DocMeta"] & {
            document: components["schemas"]["ResourceDocument"];
            /** @description The version this one was restored from. */
            restoredFrom: number | null;
        };
        ResourceVersionInfo: {
            version: number;
            /** Format: date-time */
            createdAt: string;
            createdBy: components["schemas"]["Actor"];
            restoredFrom: number | null;
        };
        ResourceVersionList: {
            items: components["schemas"]["ResourceVersionInfo"][];
            nextCursor: string | null;
        };
        /** @description One stored observation; the measurements are under the field named after `type`. */
        ResourceSnapshot: {
            /** Format: int64 */
            id: number;
            resourceId: string;
            type: components["schemas"]["ResourceTypeName"];
            /** Format: date-time */
            observedAt: string;
            machine?: components["schemas"]["MachineSnapshot"];
            router?: components["schemas"]["RouterSnapshot"];
        };
        ResourceEvent: {
            /** Format: int64 */
            id: number;
            resourceId: string;
            /** @description `control`, `journal` or `health`. */
            source: string;
            kind: string;
            severity: string;
            /** Format: date-time */
            occurredAt: string;
            /** Format: date-time */
            ingestedAt: string;
            bootId?: string;
            unit?: string;
            historical: boolean;
            reason?: string;
            /** @description `reason` with a code that depends on the event; set by the event list when there is a reason. */
            reasonMessage?: components["schemas"]["Message"];
            operationId?: string;
        };
        ResourceCapability: {
            action: string;
            available: boolean;
            reason: string;
        };
        /** @description A durable operation. `request`, `receipt` and `evidence` are type-specific. */
        ResourceOperation: {
            id: string;
            resourceId: string;
            action: string;
            /** @description For example pending, submitted, succeeded, failed or indeterminate. */
            state: string;
            /** @description The outcome is not known yet or needs `resolve`. */
            unresolved: boolean;
            /** @description Document version the operation was submitted against. */
            version: number;
            /** Format: date-time */
            createdAt: string;
            /** Format: date-time */
            updatedAt: string;
            actor: components["schemas"]["Actor"];
            request: {
                [key: string]: unknown;
            };
            receipt: {
                [key: string]: unknown;
            } | null;
            /** @description Explains the current state. */
            reason: string;
            /** @description The operator's reason for the request. */
            requestReason?: string;
            resolution?: string;
            resolvedBy?: components["schemas"]["Actor"];
            /** Format: date-time */
            resolvedAt?: string;
            evidence?: {
                [key: string]: unknown;
            };
        };
        /** @description WAN link and traffic; addresses, DNS servers and MAC addresses are never read. */
        RouterWAN: {
            link: boolean | null;
            status: number | null;
            uptimeSeconds: number | null;
            /** @description Connection type the router reports, such as `dhcp` or `pppoe`. */
            type?: string;
            upBytesPerSec: number | null;
            downBytesPerSec: number | null;
            /** Format: int64 */
            uploadedBytes: number | null;
            /** Format: int64 */
            downloadedBytes: number | null;
        };
        /** @description One observation attempt; measurements the router did not report are null. */
        RouterSnapshot: {
            /** Format: int64 */
            id: number;
            /** Format: date-time */
            observedAt: string;
            /**
             * @description `unreachable`: no HTTP answer; `unavailable`: an answer without usable status; `not-configured`: no usable connection or password secret on this host.
             * @enum {string}
             */
            outcome: "ok" | "unreachable" | "auth-failed" | "unavailable" | "not-configured";
            /** @description Fixed description of a failed attempt. */
            reason?: string;
            cpuPercent: number | null;
            cpuCores: number | null;
            memoryPercent: number | null;
            uptimeSeconds: number | null;
            temperatureC: number | null;
            /** @description Online clients. */
            clients: number | null;
            wan: components["schemas"]["RouterWAN"];
            /** @description Availability of each status source (`status`, `wan`). */
            sources: {
                [key: string]: components["schemas"]["MachineCoverage"];
            };
            model: string;
            firmware: string;
        };
        RouterStatus: {
            /** @description The latest observation attempt. */
            snapshot: components["schemas"]["RouterSnapshot"] | null;
            health: components["schemas"]["Health"];
            reasons: string[];
            /** @description `reasons` with codes (`health.*`), in the same order. */
            reasonMessages: components["schemas"]["Message"][];
            /** @description The latest attempt is younger than `staleAfter`. */
            fresh: boolean;
            /** @enum {string} */
            reachability: "reachable" | "unreachable" | "unknown";
            /**
             * Format: date-time
             * @description Last boot estimated from the reported uptime.
             */
            bootAt: string | null;
        };
        Connection: {
            name: string;
            /** @enum {string} */
            type: "xiaomi";
            /** @description Write actions resources may perform through the connection; others are refused. */
            allowActions: string[];
            /** @description Resource names that may use the connection; empty allows none. */
            allowResources: string[];
            /** @description Managed and retired resources whose document names the connection. */
            resources: string[];
        };
        /** @description Payload of `resource` events. */
        ResourceChange: {
            id: string;
            name: string;
            type: components["schemas"]["ResourceTypeName"];
            /** @enum {string} */
            change: "created" | "updated" | "restored" | "phase" | "unmanaged";
        };
    };
    responses: {
        /** @description Internal server error. */
        Internal: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["ErrorEnvelope"];
            };
        };
        /** @description Component disabled, capacity exceeded or shutting down. */
        Unavailable: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["ErrorEnvelope"];
            };
        };
        /** @description The request is malformed. */
        BadRequest: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["ErrorEnvelope"];
            };
        };
        /** @description Not authenticated. */
        Unauthorized: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["ErrorEnvelope"];
            };
        };
        /** @description Authenticated but not allowed, for example a token scope that does not permit the endpoint. */
        Forbidden: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["ErrorEnvelope"];
            };
        };
        /** @description The resource does not exist. */
        NotFound: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["ErrorEnvelope"];
            };
        };
        /**
         * @description Conflict. `error.code` tells which: `version_conflict` (details carry
         *     `currentVersion`), `already_exists`, `lock_held`, `invalid_state`,
         *     `precondition_failed`, `impact_unacknowledged` (details carry `resource`,
         *     `action` and `impact`, a `ResourceImpact`) or `conflict`.
         */
        Conflict: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["ErrorEnvelope"];
            };
        };
        /** @description The request body exceeds the limit. */
        TooLarge: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["ErrorEnvelope"];
            };
        };
        /** @description Validation failed; `error.details.issues` locates the problems. */
        Unprocessable: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["ValidationErrorEnvelope"];
            };
        };
        /** @description Too many failed attempts. */
        TooManyRequests: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": components["schemas"]["ErrorEnvelope"];
            };
        };
        /** @description Recorded. */
        HeartbeatAccepted: {
            headers: {
                [name: string]: unknown;
            };
            content: {
                "application/json": {
                    accepted: boolean;
                };
            };
        };
    };
    parameters: {
        /** @description Page size. */
        Limit: number;
        /** @description The `nextCursor` of the previous page. */
        Cursor: string;
        /** @description The version the change is based on, or `*` to create. */
        IfMatch: string;
        /** @description Name of an active resource, or `@<id>` for any incarnation including archives. */
        Resource: string;
        Policy: string;
        Module: string;
        SourceName: string;
        OwnedKind: "resource" | "policy" | "module";
        OwnedName: string;
        Version: number;
        RunID: string;
        /** @description Entry kinds to keep, repeated or comma separated. */
        TimelineKinds: string[];
        /** @description Entries at or after this time. */
        TimelineFrom: string;
        /** @description Entries before this time. */
        TimelineTo: string;
    };
    requestBodies: {
        ResourceDocument: {
            content: {
                "application/yaml": string;
                "application/json": components["schemas"]["ResourceDocument"];
            };
        };
        PolicyDocument: {
            content: {
                "application/yaml": string;
                "application/json": components["schemas"]["PolicyDocument"];
            };
        };
        ModuleDocument: {
            content: {
                "application/yaml": string;
                "application/json": components["schemas"]["ModuleDocument"];
            };
        };
    };
    headers: {
        /** @description The document version, quoted. */
        ETag: string;
    };
    pathItems: never;
}
export type $defs = Record<string, never>;
export interface operations {
    getHealth: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Healthy. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ServerHealth"];
                };
            };
            /** @description The database is unavailable. */
            503: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ErrorEnvelope"];
                };
            };
        };
    };
    getOpenAPI: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description The OpenAPI document. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/yaml": string;
                };
            };
        };
    };
    login: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["LoginRequest"];
            };
        };
        responses: {
            /** @description Logged in. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["LoginResponse"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            429: components["responses"]["TooManyRequests"];
        };
    };
    logout: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Logged out. */
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
        };
    };
    getMe: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description The caller. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": {
                        actor: components["schemas"]["Actor"];
                    };
                };
            };
            401: components["responses"]["Unauthorized"];
        };
    };
    listTokens: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Tokens, newest first. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": {
                        items: components["schemas"]["Token"][];
                    };
                };
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
        };
    };
    createToken: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["TokenCreateRequest"];
            };
        };
        responses: {
            /** @description Created. */
            201: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["TokenCreated"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            422: components["responses"]["Unprocessable"];
        };
    };
    revokeToken: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: number;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Revoked. */
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
        };
    };
    listAudit: {
        parameters: {
            query?: {
                limit?: number;
                /** @description Return entries with an id below this one. */
                before?: number;
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Entries, newest first. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": {
                        items: components["schemas"]["AuditEntry"][];
                        /** Format: int64 */
                        nextBefore: number | null;
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
        };
    };
    getAction: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description The action. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Action"];
                };
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
        };
    };
    listCheckTypes: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Check types. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": {
                        items: components["schemas"]["CheckType"][];
                    };
                };
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
        };
    };
    listAlerts: {
        parameters: {
            query?: {
                /** @description Page size. */
                limit?: components["parameters"]["Limit"];
                /** @description The `nextCursor` of the previous page. */
                cursor?: components["parameters"]["Cursor"];
                /** @description Only alerts of this resource. */
                resource?: string;
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Alerts, oldest first. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["AlertList"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
        };
    };
    listResourceTypes: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Resource types in display order. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": {
                        items: components["schemas"]["ResourceType"][];
                    };
                };
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
        };
    };
    listResources: {
        parameters: {
            query?: {
                /** @description Page size. */
                limit?: components["parameters"]["Limit"];
                /** @description The `nextCursor` of the previous page. */
                cursor?: components["parameters"]["Cursor"];
                type?: "service" | "machine" | "router";
                /** @description Keep resources with this label, as `key=value`. May be repeated; all must match. */
                label?: string[];
                phase?: "managed" | "retired";
                /** @description Type-specific state, such as `running` or `online`. */
                state?: string;
                health?: components["schemas"]["Health"];
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Resources ordered by name. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ResourceList"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
        };
    };
    createResource: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: components["requestBodies"]["ResourceDocument"];
        responses: {
            /** @description Created. */
            201: {
                headers: {
                    ETag: components["headers"]["ETag"];
                    Location?: string;
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ResourceDetail"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["TooLarge"];
            422: components["responses"]["Unprocessable"];
        };
    };
    listResourceArchives: {
        parameters: {
            query?: {
                /** @description Page size. */
                limit?: components["parameters"]["Limit"];
                /** @description The `nextCursor` of the previous page. */
                cursor?: components["parameters"]["Cursor"];
                type?: "service" | "machine" | "router";
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Archives ordered by name, newest first. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ResourceList"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
        };
    };
    getResource: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description Name of an active resource, or `@<id>` for any incarnation including archives. */
                resource: components["parameters"]["Resource"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description The resource. */
            200: {
                headers: {
                    ETag: components["headers"]["ETag"];
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ResourceDetail"];
                };
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
        };
    };
    updateResource: {
        parameters: {
            query?: never;
            header: {
                "If-Match": string;
            };
            path: {
                /** @description Name of an active resource, or `@<id>` for any incarnation including archives. */
                resource: components["parameters"]["Resource"];
            };
            cookie?: never;
        };
        requestBody: components["requestBodies"]["ResourceDocument"];
        responses: {
            /** @description Saved. An unchanged document keeps its version. */
            200: {
                headers: {
                    ETag: components["headers"]["ETag"];
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ResourceDetail"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["TooLarge"];
            422: components["responses"]["Unprocessable"];
        };
    };
    listResourceVersions: {
        parameters: {
            query?: {
                /** @description Page size. */
                limit?: components["parameters"]["Limit"];
                /** @description The `nextCursor` of the previous page. */
                cursor?: components["parameters"]["Cursor"];
            };
            header?: never;
            path: {
                /** @description Name of an active resource, or `@<id>` for any incarnation including archives. */
                resource: components["parameters"]["Resource"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Versions, newest first. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ResourceVersionList"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
        };
    };
    getResourceVersion: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description Name of an active resource, or `@<id>` for any incarnation including archives. */
                resource: components["parameters"]["Resource"];
                v: components["parameters"]["Version"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description The version. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ResourceVersion"];
                };
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
        };
    };
    restoreResource: {
        parameters: {
            query?: never;
            header?: {
                /** @description The current version; the current one is used when omitted. */
                "If-Match"?: string;
            };
            path: {
                /** @description Name of an active resource, or `@<id>` for any incarnation including archives. */
                resource: components["parameters"]["Resource"];
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": {
                    version: number;
                };
            };
        };
        responses: {
            /** @description The resource at its new current version. */
            200: {
                headers: {
                    ETag: components["headers"]["ETag"];
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ResourceDetail"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["Unprocessable"];
        };
    };
    setResourcePhase: {
        parameters: {
            query?: never;
            header?: {
                /** @description The current version, when the body does not carry it. */
                "If-Match"?: string;
            };
            path: {
                /** @description Name of an active resource, or `@<id>` for any incarnation including archives. */
                resource: components["parameters"]["Resource"];
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["ResourcePhaseRequest"];
            };
        };
        responses: {
            /** @description The resource in its new phase. */
            200: {
                headers: {
                    ETag: components["headers"]["ETag"];
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ResourceDetail"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["Unprocessable"];
        };
    };
    runResourceAction: {
        parameters: {
            query?: {
                dryRun?: boolean;
            };
            header?: never;
            path: {
                /** @description Name of an active resource, or `@<id>` for any incarnation including archives. */
                resource: components["parameters"]["Resource"];
                action: components["schemas"]["ActionName"];
            };
            cookie?: never;
        };
        requestBody?: {
            content: {
                "application/json": components["schemas"]["ActionRequest"];
            };
        };
        responses: {
            /** @description The plan (only with `dryRun=true`). */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ActionPlan"];
                };
            };
            /** @description Started. */
            202: {
                headers: {
                    Location?: string;
                    [name: string]: unknown;
                };
                content: {
                    "application/json": {
                        actionId: string;
                        /** @enum {string} */
                        status: "running";
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["Unprocessable"];
        };
    };
    getResourceImpact: {
        parameters: {
            query?: {
                /** @description An action, operation or phase change of the resource's type (see `GET /resource-types`). */
                action?: string;
            };
            header?: never;
            path: {
                /** @description Name of an active resource, or `@<id>` for any incarnation including archives. */
                resource: components["parameters"]["Resource"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description The impact. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ResourceImpact"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
        };
    };
    runResourceGroupAction: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["ResourceGroupActionRequest"];
            };
        };
        responses: {
            /** @description The plan (only with `dryRun`). */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ResourceGroupPlan"];
                };
            };
            /** @description Started. */
            202: {
                headers: {
                    Location?: string;
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ResourceGroupAction"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["TooLarge"];
            422: components["responses"]["Unprocessable"];
        };
    };
    getResourceGroupAction: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description The group action. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ResourceGroupAction"];
                };
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
        };
    };
    listDeployments: {
        parameters: {
            query?: {
                /** @description Page size. */
                limit?: components["parameters"]["Limit"];
                /** @description The `nextCursor` of the previous page. */
                cursor?: components["parameters"]["Cursor"];
            };
            header?: never;
            path: {
                /** @description Name of an active resource, or `@<id>` for any incarnation including archives. */
                resource: components["parameters"]["Resource"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Deployments of the resource's name, newest first; empty for other types. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["DeploymentList"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
        };
    };
    listResourceTimeline: {
        parameters: {
            query?: {
                /** @description Page size. */
                limit?: components["parameters"]["Limit"];
                /** @description The `nextCursor` of the previous page. */
                cursor?: components["parameters"]["Cursor"];
                /** @description Entry kinds to keep, repeated or comma separated. */
                kinds?: components["parameters"]["TimelineKinds"];
                /** @description Entries at or after this time. */
                from?: components["parameters"]["TimelineFrom"];
                /** @description Entries before this time. */
                to?: components["parameters"]["TimelineTo"];
            };
            header?: never;
            path: {
                /** @description Name of an active resource, or `@<id>` for any incarnation including archives. */
                resource: components["parameters"]["Resource"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Entries, newest first. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["TimelineList"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
        };
    };
    listTimeline: {
        parameters: {
            query?: {
                /** @description Page size. */
                limit?: components["parameters"]["Limit"];
                /** @description The `nextCursor` of the previous page. */
                cursor?: components["parameters"]["Cursor"];
                /** @description Only entries of this resource. */
                resource?: string;
                /** @description Entry kinds to keep, repeated or comma separated. */
                kinds?: components["parameters"]["TimelineKinds"];
                /** @description Entries at or after this time. */
                from?: components["parameters"]["TimelineFrom"];
                /** @description Entries before this time. */
                to?: components["parameters"]["TimelineTo"];
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Entries, newest first. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["TimelineList"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
        };
    };
    listChecks: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description Name of an active resource, or `@<id>` for any incarnation including archives. */
                resource: components["parameters"]["Resource"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Checks; empty for a resource without checks. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Checks"];
                };
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
        };
    };
    getCheckSeries: {
        parameters: {
            query?: {
                /** @description Start of the range; one hour before `to` when omitted. */
                from?: string;
                /** @description End of the range (exclusive); now when omitted. */
                to?: string;
                /** @description Maximum number of points. */
                points?: number;
            };
            header?: never;
            path: {
                /** @description Name of an active resource, or `@<id>` for any incarnation including archives. */
                resource: components["parameters"]["Resource"];
                check: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description The series. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Series"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
        };
    };
    getResourceSummary: {
        parameters: {
            query?: {
                window?: "24h" | "7d" | "30d";
            };
            header?: never;
            path: {
                /** @description Name of an active resource, or `@<id>` for any incarnation including archives. */
                resource: components["parameters"]["Resource"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Totals for the resource and per check. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ResourceSummaryStats"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
        };
    };
    observeResource: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description Name of an active resource, or `@<id>` for any incarnation including archives. */
                resource: components["parameters"]["Resource"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description The resource after the observation. */
            200: {
                headers: {
                    ETag: components["headers"]["ETag"];
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ResourceDetail"];
                };
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            503: components["responses"]["Unavailable"];
        };
    };
    listResourceHistory: {
        parameters: {
            query?: {
                /** @description Page size. */
                limit?: components["parameters"]["Limit"];
                /** @description The `nextCursor` of the previous page. */
                cursor?: components["parameters"]["Cursor"];
                resolution?: "raw" | "minute";
            };
            header?: never;
            path: {
                /** @description Name of an active resource, or `@<id>` for any incarnation including archives. */
                resource: components["parameters"]["Resource"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Snapshots, oldest first; at most 199 per page. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": {
                        items: components["schemas"]["ResourceSnapshot"][];
                        nextCursor: string | null;
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
        };
    };
    listResourceEvents: {
        parameters: {
            query?: {
                /** @description Page size. */
                limit?: components["parameters"]["Limit"];
                /** @description The `nextCursor` of the previous page. */
                cursor?: components["parameters"]["Cursor"];
            };
            header?: never;
            path: {
                /** @description Name of an active resource, or `@<id>` for any incarnation including archives. */
                resource: components["parameters"]["Resource"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Events, oldest first; at most 199 per page. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": {
                        items: components["schemas"]["ResourceEvent"][];
                        nextCursor: string | null;
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
        };
    };
    getResourceCapabilities: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description Name of an active resource, or `@<id>` for any incarnation including archives. */
                resource: components["parameters"]["Resource"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description One entry per operation. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": {
                        items: components["schemas"]["ResourceCapability"][];
                    };
                };
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
        };
    };
    getResourceMaintenance: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description Name of an active resource, or `@<id>` for any incarnation including archives. */
                resource: components["parameters"]["Resource"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description The active maintenance, or null. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": {
                        maintenance: components["schemas"]["MachineMaintenance"] | null;
                    };
                };
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            503: components["responses"]["Unavailable"];
        };
    };
    enterResourceMaintenance: {
        parameters: {
            query?: never;
            header: {
                "If-Match": string;
            };
            path: {
                /** @description Name of an active resource, or `@<id>` for any incarnation including archives. */
                resource: components["parameters"]["Resource"];
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": {
                    reason: string;
                    /** @description The `hash` of the reviewed preview; entry is refused (409) when the workloads changed since. */
                    previewHash?: string;
                };
            };
        };
        responses: {
            /** @description The active maintenance. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": {
                        maintenance: components["schemas"]["MachineMaintenance"];
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["Unprocessable"];
            503: components["responses"]["Unavailable"];
        };
    };
    previewResourceMaintenance: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description Name of an active resource, or `@<id>` for any incarnation including archives. */
                resource: components["parameters"]["Resource"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Current known local workloads and partial coverage; review before entering maintenance. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["MachineMaintenancePreview"];
                };
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            503: components["responses"]["Unavailable"];
        };
    };
    exitResourceMaintenance: {
        parameters: {
            query?: never;
            header: {
                "If-Match": string;
            };
            path: {
                /** @description Name of an active resource, or `@<id>` for any incarnation including archives. */
                resource: components["parameters"]["Resource"];
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": {
                    reason: string;
                };
            };
        };
        responses: {
            /** @description Exited. */
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["Unprocessable"];
            503: components["responses"]["Unavailable"];
        };
    };
    listResourceOperations: {
        parameters: {
            query?: {
                /** @description Page size. */
                limit?: components["parameters"]["Limit"];
                /** @description The `nextCursor` of the previous page. */
                cursor?: components["parameters"]["Cursor"];
            };
            header?: never;
            path: {
                /** @description Name of an active resource, or `@<id>` for any incarnation including archives. */
                resource: components["parameters"]["Resource"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Operations, newest first. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": {
                        items: components["schemas"]["ResourceOperation"][];
                        nextCursor: string | null;
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
        };
    };
    submitResourceOperation: {
        parameters: {
            query?: never;
            header: {
                "If-Match": string;
            };
            path: {
                /** @description Name of an active resource, or `@<id>` for any incarnation including archives. */
                resource: components["parameters"]["Resource"];
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": {
                    /** @description For example `reboot` or `graceful-shutdown`; see `GET /resource-types`. */
                    action: string;
                    /** @description Chosen by the client: 1-128 letters, digits, `_`, `.`, `:` or `-`, starting with a letter or digit. */
                    idempotencyKey: string;
                    /** @description Why the operation is requested; recorded with the operation and in the audit log. */
                    reason: string;
                    /** @description The resource's exact name */
                    confirm: string;
                    /** @description Accept the impact reported by `GET /resources/{resource}/impact?action=`. */
                    acknowledgeImpact?: boolean;
                };
            };
        };
        responses: {
            /** @description The stored operation. */
            202: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ResourceOperation"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["Unprocessable"];
        };
    };
    getResourceOperation: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description Name of an active resource, or `@<id>` for any incarnation including archives. */
                resource: components["parameters"]["Resource"];
                id: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description The operation. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ResourceOperation"];
                };
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
        };
    };
    resolveResourceOperation: {
        parameters: {
            query?: never;
            header: {
                "If-Match": string;
            };
            path: {
                /** @description Name of an active resource, or `@<id>` for any incarnation including archives. */
                resource: components["parameters"]["Resource"];
                id: string;
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": {
                    reason: string;
                };
            };
        };
        responses: {
            /** @description The resolved operation. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ResourceOperation"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["Unprocessable"];
        };
    };
    listConnections: {
        parameters: {
            query?: {
                /** @description Page size. */
                limit?: components["parameters"]["Limit"];
                /** @description The `nextCursor` of the previous page. */
                cursor?: components["parameters"]["Cursor"];
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Connections in file order. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": {
                        items: components["schemas"]["Connection"][];
                        nextCursor: string | null;
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
        };
    };
    listPolicies: {
        parameters: {
            query?: {
                /** @description Page size. */
                limit?: components["parameters"]["Limit"];
                /** @description The `nextCursor` of the previous page. */
                cursor?: components["parameters"]["Cursor"];
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Policies ordered by name, deployed or not. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["PolicyList"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
        };
    };
    getPolicy: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                p: components["parameters"]["Policy"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description The policy. */
            200: {
                headers: {
                    ETag: components["headers"]["ETag"];
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["PolicyDetail"];
                };
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
        };
    };
    deployPolicy: {
        parameters: {
            query?: never;
            header?: {
                /** @description The version the change is based on, or `*` to create. */
                "If-Match"?: components["parameters"]["IfMatch"];
            };
            path: {
                p: components["parameters"]["Policy"];
            };
            cookie?: never;
        };
        requestBody: components["requestBodies"]["PolicyDocument"];
        responses: {
            /** @description Deployed a new or unchanged version of an existing policy. */
            200: {
                headers: {
                    ETag: components["headers"]["ETag"];
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["PolicyDetail"];
                };
            };
            /** @description Created and deployed. */
            201: {
                headers: {
                    ETag: components["headers"]["ETag"];
                    Location?: string;
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["PolicyDetail"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["TooLarge"];
            422: components["responses"]["Unprocessable"];
        };
    };
    undeployPolicy: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                p: components["parameters"]["Policy"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Undeployed. */
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
        };
    };
    pausePolicy: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                p: components["parameters"]["Policy"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Paused. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["PauseState"];
                };
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
        };
    };
    resumePolicy: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                p: components["parameters"]["Policy"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Resumed. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["PauseState"];
                };
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
        };
    };
    listPolicyVersions: {
        parameters: {
            query?: {
                /** @description Page size. */
                limit?: components["parameters"]["Limit"];
                /** @description The `nextCursor` of the previous page. */
                cursor?: components["parameters"]["Cursor"];
            };
            header?: never;
            path: {
                p: components["parameters"]["Policy"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Versions, newest first. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["DocVersionList"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
        };
    };
    getPolicyVersion: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                p: components["parameters"]["Policy"];
                v: components["parameters"]["Version"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description The version. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["PolicyDoc"];
                };
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
        };
    };
    listPolicyState: {
        parameters: {
            query?: {
                /** @description Page size. */
                limit?: components["parameters"]["Limit"];
                /** @description The `nextCursor` of the previous page. */
                cursor?: components["parameters"]["Cursor"];
            };
            header?: never;
            path: {
                p: components["parameters"]["Policy"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Entries ordered by key. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["StateList"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
        };
    };
    deletePolicyState: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                p: components["parameters"]["Policy"];
                key: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Deleted. */
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
        };
    };
    startRun: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                p: components["parameters"]["Policy"];
            };
            cookie?: never;
        };
        requestBody?: {
            content: {
                "application/json": components["schemas"]["StartRunRequest"];
            };
        };
        responses: {
            /** @description The run is stored and starts (or queues) in the background. */
            202: {
                headers: {
                    Location?: string;
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["RunStarted"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["Unprocessable"];
        };
    };
    validateDocument: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/yaml": string;
                "application/json": Record<string, never>;
            };
        };
        responses: {
            /** @description The verdict. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ValidationResult"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            413: components["responses"]["TooLarge"];
        };
    };
    listModules: {
        parameters: {
            query?: {
                /** @description Page size. */
                limit?: components["parameters"]["Limit"];
                /** @description The `nextCursor` of the previous page. */
                cursor?: components["parameters"]["Cursor"];
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Modules ordered by name. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ModuleList"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
        };
    };
    getModule: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                m: components["parameters"]["Module"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description The module. */
            200: {
                headers: {
                    ETag: components["headers"]["ETag"];
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ModuleDoc"];
                };
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
        };
    };
    saveModule: {
        parameters: {
            query?: never;
            header?: {
                /** @description The version the change is based on, or `*` to create. */
                "If-Match"?: components["parameters"]["IfMatch"];
            };
            path: {
                m: components["parameters"]["Module"];
            };
            cookie?: never;
        };
        requestBody: components["requestBodies"]["ModuleDocument"];
        responses: {
            /** @description Saved. */
            200: {
                headers: {
                    ETag: components["headers"]["ETag"];
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ModuleDoc"];
                };
            };
            /** @description Created. */
            201: {
                headers: {
                    ETag: components["headers"]["ETag"];
                    Location?: string;
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ModuleDoc"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["TooLarge"];
            422: components["responses"]["Unprocessable"];
        };
    };
    listModuleVersions: {
        parameters: {
            query?: {
                /** @description Page size. */
                limit?: components["parameters"]["Limit"];
                /** @description The `nextCursor` of the previous page. */
                cursor?: components["parameters"]["Cursor"];
            };
            header?: never;
            path: {
                m: components["parameters"]["Module"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Versions, newest first. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["DocVersionList"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
        };
    };
    listNodeTypes: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Node types sorted by type. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": {
                        items: components["schemas"]["NodeType"][];
                    };
                };
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
        };
    };
    listTemplates: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Templates sorted by id. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": {
                        items: components["schemas"]["Template"][];
                    };
                };
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
        };
    };
    instantiateTemplate: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description Template id. */
                t: string;
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": {
                    /** @description Name of the new policy. */
                    name: string;
                    params?: {
                        [key: string]: unknown;
                    };
                    dryRun?: boolean;
                };
            };
        };
        responses: {
            /** @description Dry run; the document that would be deployed. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["TemplateInstance"];
                };
            };
            /** @description Created and deployed. */
            201: {
                headers: {
                    Location?: string;
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["TemplateInstance"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["TooLarge"];
            422: components["responses"]["Unprocessable"];
        };
    };
    applyDocuments: {
        parameters: {
            query?: {
                dryRun?: boolean;
                force?: boolean;
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/yaml": string;
            };
        };
        responses: {
            /** @description One result per document. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ApplyResponse"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            413: components["responses"]["TooLarge"];
            422: components["responses"]["Unprocessable"];
        };
    };
    exportDocuments: {
        parameters: {
            query?: {
                /** @description resource, policy or module (plural and any case are accepted). Omit for everything. */
                kind?: string;
                /** @description One resource of `kind`. */
                name?: string;
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description The documents. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/yaml": string;
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
        };
    };
    listConfigSources: {
        parameters: {
            query?: {
                /** @description Page size. */
                limit?: components["parameters"]["Limit"];
                /** @description The `nextCursor` of the previous page. */
                cursor?: components["parameters"]["Cursor"];
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Sources ordered by name. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ConfigSourceList"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
        };
    };
    createConfigSource: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["ConfigSourceRequest"];
            };
        };
        responses: {
            /** @description Created. */
            201: {
                headers: {
                    Location?: string;
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ConfigSource"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["Unprocessable"];
        };
    };
    getConfigSource: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                name: components["parameters"]["SourceName"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description The source. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ConfigSource"];
                };
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
        };
    };
    replaceConfigSource: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                name: components["parameters"]["SourceName"];
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["ConfigSourceRequest"];
            };
        };
        responses: {
            /** @description Updated. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ConfigSource"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            422: components["responses"]["Unprocessable"];
        };
    };
    deleteConfigSource: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                name: components["parameters"]["SourceName"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Deleted. */
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
        };
    };
    syncConfigSource: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                name: components["parameters"]["SourceName"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description The sync is running or was already running. */
            202: {
                headers: {
                    Location?: string;
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ConfigSource"];
                };
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
        };
    };
    listConfigOwnership: {
        parameters: {
            query?: {
                /** @description Page size. */
                limit?: components["parameters"]["Limit"];
                /** @description The `nextCursor` of the previous page. */
                cursor?: components["parameters"]["Cursor"];
                source?: string;
                kind?: string;
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Managed resources ordered by kind and name. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["OwnershipList"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
        };
    };
    resolveConfigOwnership: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                kind: components["parameters"]["OwnedKind"];
                name: components["parameters"]["OwnedName"];
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": {
                    /** @enum {string} */
                    use: "git" | "current";
                };
            };
        };
        responses: {
            /** @description The resource's ownership after resolving. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Ownership"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["Unprocessable"];
        };
    };
    releaseConfigOwnership: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                kind: components["parameters"]["OwnedKind"];
                name: components["parameters"]["OwnedName"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Released. */
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
        };
    };
    listRuns: {
        parameters: {
            query?: {
                /** @description Page size. */
                limit?: components["parameters"]["Limit"];
                /** @description The `nextCursor` of the previous page. */
                cursor?: components["parameters"]["Cursor"];
                policy?: string;
                /** @description One or more statuses, repeated or comma separated. */
                status?: components["schemas"]["RunStatus"][];
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Runs, newest first. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["RunList"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
        };
    };
    getRun: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["RunID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description The run. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["Run"];
                };
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
        };
    };
    cancelRun: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["RunID"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Cancellation requested; the run ends as `cancelled`. */
            202: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["RunRef"];
                };
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
        };
    };
    resumeNode: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: components["parameters"]["RunID"];
                /** @description Scoped node id. */
                node: string;
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["ResumeRequest"];
            };
        };
        responses: {
            /** @description Resumed; the node continues in the background. */
            202: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": {
                        runId: string;
                        node: string;
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            422: components["responses"]["Unprocessable"];
        };
    };
    listLocks: {
        parameters: {
            query?: {
                /** @description Page size. */
                limit?: components["parameters"]["Limit"];
                /** @description The `nextCursor` of the previous page. */
                cursor?: components["parameters"]["Cursor"];
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Locks ordered by name. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["LockList"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
        };
    };
    forceReleaseLock: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                name: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Released. */
            204: {
                headers: {
                    [name: string]: unknown;
                };
                content?: never;
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
        };
    };
    ingestSecurityTraffic: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["SecurityTraffic"];
            };
        };
        responses: {
            /** @description Persisted observation or an identical retry */
            202: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": {
                        accepted: boolean;
                        duplicate: boolean;
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["TooLarge"];
            422: components["responses"]["Unprocessable"];
            500: components["responses"]["Internal"];
            503: components["responses"]["Unavailable"];
        };
    };
    listSecurityTraffic: {
        parameters: {
            query?: {
                /** @description Page size. */
                limit?: components["parameters"]["Limit"];
                /** @description The `nextCursor` of the previous page. */
                cursor?: components["parameters"]["Cursor"];
                machineId?: string;
                service?: string;
                category?: string;
                from?: string;
                to?: string;
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Newest receipt first, with original observation window times */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": {
                        items: (components["schemas"]["SecurityTraffic"] & {
                            id: number;
                        })[];
                        nextCursor: string | null;
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            422: components["responses"]["Unprocessable"];
            500: components["responses"]["Internal"];
            503: components["responses"]["Unavailable"];
        };
    };
    ingestSecurityEvent: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["SecurityEvent"];
            };
        };
        responses: {
            /** @description Persisted event or an identical retry */
            202: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["SecurityReceipt"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["TooLarge"];
            422: components["responses"]["Unprocessable"];
            500: components["responses"]["Internal"];
            503: components["responses"]["Unavailable"];
        };
    };
    getSecurityStatus: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Delivery counters and latest observation progress */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["SecurityStatus"];
                };
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            500: components["responses"]["Internal"];
            503: components["responses"]["Unavailable"];
        };
    };
    listSecurityIncidents: {
        parameters: {
            query?: {
                /** @description Page size. */
                limit?: components["parameters"]["Limit"];
                /** @description The `nextCursor` of the previous page. */
                cursor?: components["parameters"]["Cursor"];
                machineId?: string;
                service?: string;
                source?: "machine" | "service" | "nginx" | "auditd" | "aide" | "collector";
                severity?: "info" | "warning" | "critical";
                state?: "firing" | "resolved" | "retired";
                from?: string;
                to?: string;
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Incident page, including delivery failures and recoveries */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": {
                        items: components["schemas"]["SecurityIncident"][];
                        nextCursor: string | null;
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            422: components["responses"]["Unprocessable"];
            500: components["responses"]["Internal"];
            503: components["responses"]["Unavailable"];
        };
    };
    ingestAlert: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                source: "alertmanager" | "uptime-kuma" | "gatus" | "generic";
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": unknown;
            };
        };
        responses: {
            /** @description Accepted. */
            202: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": {
                        /** @description Alerts that started or stopped firing and were published as signals. */
                        signals: number;
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            404: components["responses"]["NotFound"];
            413: components["responses"]["TooLarge"];
        };
    };
    githubPush: {
        parameters: {
            query?: never;
            header: {
                "X-GitHub-Event": string;
            };
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": Record<string, never>;
            };
        };
        responses: {
            /** @description Accepted. */
            202: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["GitHubAck"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            413: components["responses"]["TooLarge"];
            503: components["responses"]["Unavailable"];
        };
    };
    deliverWebhook: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                name: string;
            };
            cookie?: never;
        };
        requestBody?: {
            content: {
                "*/*": unknown;
            };
        };
        responses: {
            /** @description Accepted. */
            202: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": {
                        /** @description Ids of the runs that were started. */
                        runs: string[];
                    };
                };
            };
            401: components["responses"]["Unauthorized"];
            404: components["responses"]["NotFound"];
            413: components["responses"]["TooLarge"];
            503: components["responses"]["Unavailable"];
        };
    };
    streamEvents: {
        parameters: {
            query?: {
                /** @description Only events about the resource with this name. */
                resource?: string;
                /** @description Only events that carry this immutable resource ID (resource changes, observations). */
                resourceId?: string;
                /** @description Only events about this policy. */
                policy?: string;
                /** @description Only events of this run. */
                run?: string;
                /** @description Event types to receive, repeated or comma separated; all when omitted. */
                types?: components["schemas"]["EventType"][];
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description The stream. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "text/event-stream": components["schemas"]["EventEnvelope"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
        };
    };
    getSecurityIncident: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Successful bounded response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["SecurityIncidentDetail"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["TooLarge"];
            422: components["responses"]["Unprocessable"];
            429: components["responses"]["TooManyRequests"];
            500: components["responses"]["Internal"];
            503: components["responses"]["Unavailable"];
        };
    };
    updateSecurityHandling: {
        parameters: {
            query?: never;
            header: {
                /** @description Strong quoted version; first version is "0". */
                "If-Match": string;
            };
            path: {
                id: string;
            };
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["SecurityHandlingUpdate"];
            };
        };
        responses: {
            /** @description Successful bounded response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["SecurityHandling"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["TooLarge"];
            422: components["responses"]["Unprocessable"];
            429: components["responses"]["TooManyRequests"];
            500: components["responses"]["Internal"];
            503: components["responses"]["Unavailable"];
        };
    };
    getSecuritySettings: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Successful bounded response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["SecuritySettings"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["TooLarge"];
            422: components["responses"]["Unprocessable"];
            429: components["responses"]["TooManyRequests"];
            500: components["responses"]["Internal"];
            503: components["responses"]["Unavailable"];
        };
    };
    updateSecuritySettings: {
        parameters: {
            query?: never;
            header: {
                /** @description Strong quoted version; first version is "0". */
                "If-Match": string;
            };
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["SecuritySettingsUpdate"];
            };
        };
        responses: {
            /** @description Successful bounded response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["SecuritySettings"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["TooLarge"];
            422: components["responses"]["Unprocessable"];
            429: components["responses"]["TooManyRequests"];
            500: components["responses"]["Internal"];
            503: components["responses"]["Unavailable"];
        };
    };
    startSecurityNotificationTest: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["SecurityTestRequest"];
            };
        };
        responses: {
            /** @description Successful bounded response. */
            202: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["SecurityNotificationTest"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["TooLarge"];
            422: components["responses"]["Unprocessable"];
            429: components["responses"]["TooManyRequests"];
            500: components["responses"]["Internal"];
            503: components["responses"]["Unavailable"];
        };
    };
    getSecurityNotificationTest: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Successful bounded response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["SecurityNotificationTest"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["TooLarge"];
            422: components["responses"]["Unprocessable"];
            429: components["responses"]["TooManyRequests"];
            500: components["responses"]["Internal"];
            503: components["responses"]["Unavailable"];
        };
    };
    getSecurityTrafficSeries: {
        parameters: {
            query?: {
                machineId?: string;
                service?: string;
                category?: string;
                from?: string;
                to?: string;
                bucketSeconds?: 60 | 300 | 900 | 3600;
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Successful bounded response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["SecuritySeries"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["TooLarge"];
            422: components["responses"]["Unprocessable"];
            429: components["responses"]["TooManyRequests"];
            500: components["responses"]["Internal"];
            503: components["responses"]["Unavailable"];
        };
    };
    getSecurityHost: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Successful bounded response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["SecurityHostStatus"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["TooLarge"];
            422: components["responses"]["Unprocessable"];
            429: components["responses"]["TooManyRequests"];
            500: components["responses"]["Internal"];
            503: components["responses"]["Unavailable"];
        };
    };
    previewSecurityHost: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["SecurityHostPreviewRequest"];
            };
        };
        responses: {
            /** @description Successful bounded response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["SecurityHostPreview"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["TooLarge"];
            422: components["responses"]["Unprocessable"];
            429: components["responses"]["TooManyRequests"];
            500: components["responses"]["Internal"];
            503: components["responses"]["Unavailable"];
        };
    };
    applySecurityHost: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: {
            content: {
                "application/json": components["schemas"]["SecurityHostApplyRequest"];
            };
        };
        responses: {
            /** @description Successful bounded response. */
            202: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["SecurityHostOperation"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["TooLarge"];
            422: components["responses"]["Unprocessable"];
            429: components["responses"]["TooManyRequests"];
            500: components["responses"]["Internal"];
            503: components["responses"]["Unavailable"];
        };
    };
    listSecurityHostOperations: {
        parameters: {
            query?: {
                /** @description Page size. */
                limit?: components["parameters"]["Limit"];
                /** @description The `nextCursor` of the previous page. */
                cursor?: components["parameters"]["Cursor"];
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Successful bounded response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["SecurityHostOperations"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["TooLarge"];
            422: components["responses"]["Unprocessable"];
            429: components["responses"]["TooManyRequests"];
            500: components["responses"]["Internal"];
            503: components["responses"]["Unavailable"];
        };
    };
    getSecurityHostOperation: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                id: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Successful bounded response. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["SecurityHostOperation"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["TooLarge"];
            422: components["responses"]["Unprocessable"];
            429: components["responses"]["TooManyRequests"];
            500: components["responses"]["Internal"];
            503: components["responses"]["Unavailable"];
        };
    };
    getSecuritySettingsHistory: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Durable change history. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": {
                        items: components["schemas"]["SecuritySettingsHistory"][];
                    };
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["TooLarge"];
            422: components["responses"]["Unprocessable"];
            429: components["responses"]["TooManyRequests"];
            500: components["responses"]["Internal"];
            503: components["responses"]["Unavailable"];
        };
    };
    getHeartbeat: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description Name of an active resource, or `@<id>` for any incarnation including archives. */
                resource: components["parameters"]["Resource"];
                check: string;
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            202: components["responses"]["HeartbeatAccepted"];
            401: components["responses"]["Unauthorized"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
        };
    };
    postHeartbeat: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                /** @description Name of an active resource, or `@<id>` for any incarnation including archives. */
                resource: components["parameters"]["Resource"];
                check: string;
            };
            cookie?: never;
        };
        requestBody?: {
            content: {
                "*/*": string;
            };
        };
        responses: {
            202: components["responses"]["HeartbeatAccepted"];
            401: components["responses"]["Unauthorized"];
            404: components["responses"]["NotFound"];
            409: components["responses"]["Conflict"];
            413: components["responses"]["TooLarge"];
        };
    };
}
