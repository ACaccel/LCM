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
    "/hosts": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** Hosts services run on */
        get: operations["listHosts"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/services": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** List services */
        get: operations["listServices"];
        put?: never;
        /** Create a service */
        post: operations["createService"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/services/{svc}": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                svc: components["parameters"]["Service"];
            };
            cookie?: never;
        };
        /** A service with its state, health, checks and alerts */
        get: operations["getService"];
        /**
         * Replace a service document
         * @description The name in the document must match the URL. `If-Match` is required.
         */
        put: operations["updateService"];
        post?: never;
        /**
         * Delete a service
         * @description Only services that are registered, stopped, failed or retired can be deleted. History is kept.
         */
        delete: operations["deleteService"];
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/services/{svc}/versions": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                svc: components["parameters"]["Service"];
            };
            cookie?: never;
        };
        /** Stored versions of a service document */
        get: operations["listServiceVersions"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/services/{svc}/versions/{v}": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                svc: components["parameters"]["Service"];
                v: components["parameters"]["Version"];
            };
            cookie?: never;
        };
        /** One stored version of a service document */
        get: operations["getServiceVersion"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/services/{svc}/restore": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /** Restore an old version as a new one */
        post: operations["restoreService"];
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/services/{svc}/actions/{action}": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        get?: never;
        put?: never;
        /**
         * Run a lifecycle action
         * @description Starts the action in the background and answers 202 with an action id.
         *     Preconditions that can be checked up front (unknown service, wrong state,
         *     missing configuration, busy service) fail synchronously. With
         *     `dryRun=true` nothing is started and the plan is returned.
         */
        post: operations["runServiceAction"];
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
    "/services/{svc}/deployments": {
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
    "/services/{svc}/timeline": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** Timeline of one service */
        get: operations["listServiceTimeline"];
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
        /** Timeline of all services */
        get: operations["listTimeline"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/services/{svc}/checks": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** Current status of a service's health checks */
        get: operations["listChecks"];
        put?: never;
        post?: never;
        delete?: never;
        options?: never;
        head?: never;
        patch?: never;
        trace?: never;
    };
    "/services/{svc}/checks/{check}/series": {
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
    "/services/{svc}/summary": {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        /** Uptime and latency over a window */
        get: operations["getServiceSummary"];
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
         * @description Decodes and checks a Policy, Module or Service. Problems are a normal result
         *     and answer 200 with `valid: false`; only a request that cannot be read is an
         *     error. Policies are compiled against the module store, and services are
         *     checked against the hosts and the check types.
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
         * Apply Service, Policy and Module documents
         * @description Applies a multi-document YAML stream (documents separated by `---`): modules
         *     first, then services, then policies, whatever the order in the body. It is not
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
         *     order (modules, services, policies) and by name within a kind. Only document
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
         * @description A source is a repository location LCM pulls Service, Policy and Module documents
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
    "/heartbeats/{svc}/{check}": {
        parameters: {
            query?: never;
            header?: never;
            path: {
                svc: components["parameters"]["Service"];
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
         *     `trigger.webhook` node cannot be reached under it.
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
         *     refused.
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
         *     - `signal`: a signal from the bus (check, health, lifecycle, alert).
         *     - `run`: a run event (`run.queued`, `run.started`, `run.suspended`,
         *       `run.resumed`, `run.finished`).
         *     - `node`: a node event (`node.started`, `node.finished`, `node.retrying`,
         *       `node.suspended`, `node.dead`).
         *     - `timeline`: a stored timeline entry.
         *     - `action`: progress of an action started through the API.
         *     - `catalog`: a service document was created, updated, restored or deleted,
         *       or a policy was deployed or undeployed.
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
}
export type webhooks = Record<string, never>;
export interface components {
    schemas: {
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
        Host: {
            name: string;
            /** @enum {string} */
            kind: "local" | "ssh";
            address: string;
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
        /** @description A `lcm/v1` Service. */
        ServiceDocument: {
            /** @enum {string} */
            apiVersion: "lcm/v1";
            /** @enum {string} */
            kind: "Service";
            metadata: components["schemas"]["Metadata"];
            spec: {
                host?: string;
                runtime: {
                    /** @enum {string} */
                    type: "systemd" | "docker" | "compose" | "command";
                    unit?: string;
                    scope?: string;
                    container?: string;
                    project?: string;
                    file?: string;
                    start?: string;
                    stop?: string;
                    restart?: string;
                    status?: string;
                };
                source?: {
                    repo: string;
                    remote?: string;
                    branch: string;
                    build?: string;
                    install?: string;
                    timeout?: components["schemas"]["Duration"];
                };
                backup?: components["schemas"]["Command"];
                jobs?: {
                    [key: string]: components["schemas"]["Command"];
                };
                dependsOn?: string[];
                checks?: components["schemas"]["Check"][];
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
        /** @description A `lcm/v1` Policy. */
        PolicyDocument: {
            /** @enum {string} */
            apiVersion: "lcm/v1";
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
        /** @description A `lcm/v1` Module. */
        ModuleDocument: {
            /** @enum {string} */
            apiVersion: "lcm/v1";
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
        ServiceState: "registered" | "deploying" | "running" | "updating" | "rolling_back" | "stopped" | "failed" | "retired";
        /** @enum {string} */
        Health: "unknown" | "healthy" | "degraded" | "down";
        ServiceSummary: {
            name: string;
            host: string;
            labels: components["schemas"]["Labels"];
            state: components["schemas"]["ServiceState"];
            /** Format: date-time */
            stateSince: string;
            health: components["schemas"]["Health"];
            /** @description Version of the configuration document. */
            version: number;
            /** Format: date-time */
            updatedAt: string;
            /** @description Commit of the last successful deployment; empty if none. */
            deployedVersion: string;
            managedBy: components["schemas"]["ManagedBy"];
        };
        ServiceList: {
            items: components["schemas"]["ServiceSummary"][];
            nextCursor: string | null;
        };
        ServiceDoc: components["schemas"]["DocMeta"] & {
            document: components["schemas"]["ServiceDocument"];
        };
        ServiceVersion: components["schemas"]["ServiceDoc"] & {
            /** @description The version this one was restored from. */
            restoredFrom: number | null;
        };
        ServiceDetail: components["schemas"]["ServiceDoc"] & {
            name: string;
            host: string;
            state: components["schemas"]["ServiceState"];
            /** Format: date-time */
            stateSince: string;
            /** Format: date-time */
            createdAt: string;
            health: components["schemas"]["Health"];
            /** Format: date-time */
            healthSince: string | null;
            checks: components["schemas"]["CheckStatus"][];
            alerts: components["schemas"]["Alert"][];
            deployedVersion: string;
            lastDeployment: components["schemas"]["Deployment"] | null;
            managedBy: components["schemas"]["ManagedBy"];
        };
        ServiceVersionInfo: {
            version: number;
            /** Format: date-time */
            createdAt: string;
            createdBy: components["schemas"]["Actor"];
            restoredFrom: number | null;
        };
        ServiceVersionList: {
            items: components["schemas"]["ServiceVersionInfo"][];
            nextCursor: string | null;
        };
        /** @enum {string} */
        ActionName: "deploy" | "rollback" | "start" | "stop" | "restart" | "backup" | "run-job" | "retire";
        ActionRequest: {
            /** @description Commit to deploy (7 to 64 hex digits); deploy only. The head of the configured branch when omitted. */
            version?: string;
            /** @description Name of the job; run-job only, and required there. */
            job?: string;
        };
        ActionPlan: {
            service: string;
            action: components["schemas"]["ActionName"];
            from: components["schemas"]["ServiceState"];
            to: components["schemas"]["ServiceState"];
            steps: string[];
        };
        ActionResult: {
            service: string;
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
            service: string;
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
            service: string;
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
            service: string;
            /** Format: date-time */
            time: string;
            /** @description For example `config.changed`, `lifecycle.changed`, `deployment`, `action`, `backup`, `job`, `check.changed`, `health.changed`, `alert`. */
            kind: string;
            summary: string;
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
            service: string;
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
            service: string;
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
        ServiceSummaryStats: components["schemas"]["SummaryStats"] & {
            service: string;
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
            service?: string;
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
            lastRun: components["schemas"]["LastRun"] | null;
            managedBy: components["schemas"]["ManagedBy"];
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
            kind: "Service" | "Policy" | "Module";
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
        EventType: "signal" | "run" | "node" | "timeline" | "action" | "catalog";
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
            /** @enum {string} */
            kind: "check.changed" | "health.changed" | "lifecycle.changed" | "alert";
            /** Format: date-time */
            time: string;
            service?: string;
            source: string;
            severity?: string;
            labels?: components["schemas"]["Labels"];
            data?: {
                [key: string]: unknown;
            };
        };
        /** @description Payload of `catalog` events about services. */
        CatalogChange: {
            service: string;
            /** @enum {string} */
            change: "created" | "updated" | "restored" | "deleted";
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
            service?: string;
            policy?: string;
            run?: string;
            /** @description Depends on `type`. */
            data: components["schemas"]["Signal"] | components["schemas"]["EngineEvent"] | components["schemas"]["TimelineEntry"] | components["schemas"]["Action"] | components["schemas"]["CatalogChange"];
        };
    };
    responses: {
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
         *     `precondition_failed` or `conflict`.
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
        Service: string;
        Policy: string;
        Module: string;
        SourceName: string;
        OwnedKind: "service" | "policy" | "module";
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
        ServiceDocument: {
            content: {
                "application/yaml": string;
                "application/json": components["schemas"]["ServiceDocument"];
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
    listHosts: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Hosts. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": {
                        items: components["schemas"]["Host"][];
                    };
                };
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
        };
    };
    listServices: {
        parameters: {
            query?: {
                /** @description Page size. */
                limit?: components["parameters"]["Limit"];
                /** @description The `nextCursor` of the previous page. */
                cursor?: components["parameters"]["Cursor"];
                /** @description Keep services with this label, as `key=value`. May be repeated; all must match. */
                label?: string[];
                state?: components["schemas"]["ServiceState"];
                health?: components["schemas"]["Health"];
            };
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Services ordered by name. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ServiceList"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
        };
    };
    createService: {
        parameters: {
            query?: never;
            header?: never;
            path?: never;
            cookie?: never;
        };
        requestBody: components["requestBodies"]["ServiceDocument"];
        responses: {
            /** @description Created. */
            201: {
                headers: {
                    ETag: components["headers"]["ETag"];
                    Location?: string;
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ServiceDoc"];
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
    getService: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                svc: components["parameters"]["Service"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description The service. */
            200: {
                headers: {
                    ETag: components["headers"]["ETag"];
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ServiceDetail"];
                };
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
        };
    };
    updateService: {
        parameters: {
            query?: never;
            header?: {
                /** @description The version the change is based on, or `*` to create. */
                "If-Match"?: components["parameters"]["IfMatch"];
            };
            path: {
                svc: components["parameters"]["Service"];
            };
            cookie?: never;
        };
        requestBody: components["requestBodies"]["ServiceDocument"];
        responses: {
            /** @description Saved. An unchanged document keeps its version. */
            200: {
                headers: {
                    ETag: components["headers"]["ETag"];
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ServiceDoc"];
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
    deleteService: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                svc: components["parameters"]["Service"];
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
            409: components["responses"]["Conflict"];
        };
    };
    listServiceVersions: {
        parameters: {
            query?: {
                /** @description Page size. */
                limit?: components["parameters"]["Limit"];
                /** @description The `nextCursor` of the previous page. */
                cursor?: components["parameters"]["Cursor"];
            };
            header?: never;
            path: {
                svc: components["parameters"]["Service"];
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
                    "application/json": components["schemas"]["ServiceVersionList"];
                };
            };
            400: components["responses"]["BadRequest"];
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
        };
    };
    getServiceVersion: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                svc: components["parameters"]["Service"];
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
                    "application/json": components["schemas"]["ServiceVersion"];
                };
            };
            401: components["responses"]["Unauthorized"];
            403: components["responses"]["Forbidden"];
            404: components["responses"]["NotFound"];
        };
    };
    restoreService: {
        parameters: {
            query?: never;
            header?: {
                /** @description The current version; the current one is used when omitted. */
                "If-Match"?: string;
            };
            path: {
                svc: components["parameters"]["Service"];
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
            /** @description The new current version. */
            200: {
                headers: {
                    ETag: components["headers"]["ETag"];
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ServiceDoc"];
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
    runServiceAction: {
        parameters: {
            query?: {
                dryRun?: boolean;
            };
            header?: never;
            path: {
                svc: components["parameters"]["Service"];
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
                svc: components["parameters"]["Service"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Deployments, newest first. */
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
        };
    };
    listServiceTimeline: {
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
                svc: components["parameters"]["Service"];
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
        };
    };
    listTimeline: {
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
                svc: components["parameters"]["Service"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Checks; empty for a service without checks. */
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
                svc: components["parameters"]["Service"];
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
    getServiceSummary: {
        parameters: {
            query?: {
                window?: "24h" | "7d" | "30d";
            };
            header?: never;
            path: {
                svc: components["parameters"]["Service"];
            };
            cookie?: never;
        };
        requestBody?: never;
        responses: {
            /** @description Totals for the service and per check. */
            200: {
                headers: {
                    [name: string]: unknown;
                };
                content: {
                    "application/json": components["schemas"]["ServiceSummaryStats"];
                };
            };
            400: components["responses"]["BadRequest"];
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
                /** @description Only alerts of this service. */
                service?: string;
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
                /** @description service, policy or module (plural and any case are accepted). Omit for everything. */
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
    getHeartbeat: {
        parameters: {
            query?: never;
            header?: never;
            path: {
                svc: components["parameters"]["Service"];
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
                svc: components["parameters"]["Service"];
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
        };
    };
    streamEvents: {
        parameters: {
            query?: {
                /** @description Only events about this service. */
                service?: string;
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
}
