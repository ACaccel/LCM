/**
 * ServicePanel.tsx
 *
 * Left-panel: service list + policy list for the selected service.
 * Provides create/open/delete for services and create/open/delete for
 * policies within a service.
 */

import { useState, useEffect, useCallback, useRef } from 'react';
import {
  listServices,
  getService,
  createService,
  deleteService,
  deletePolicy,
  deleteModule,
  uploadServiceConfig,
} from '../api/lcmApi';
import { useStore } from '../hooks/useStore';
import type { ServiceConfig } from '../model/types';
import '../styles/ServicePanel.css';

export default function ServicePanel() {
  const [services, setServices] = useState<string[]>([]);
  const [policies, setPolicies] = useState<string[]>([]);
  const [loading, setLoading] = useState(false);
  const [newSvcName, setNewSvcName] = useState('');
  const [newPolName, setNewPolName] = useState('');
  const [showNewSvc, setShowNewSvc] = useState(false);
  const [showNewPol, setShowNewPol] = useState(false);
  const [modules, setModules] = useState<string[]>([]);
  const [newModName, setNewModName] = useState('');
  const [showNewMod, setShowNewMod] = useState(false);
  const [uploading, setUploading] = useState(false);
  const uploadInputRef = useRef<HTMLInputElement>(null);

  const {
    selectedService,
    selectedPolicy,
    selectedModule,
    selectService,
    selectPolicy,
    selectModule,
    pushToast,
    bumpCanvasRevision,
    canvasRevision,
  } = useStore();

  const refreshServices = useCallback(async () => {
    try {
      const names = await listServices();
      setServices(names ?? []);
    } catch (err) {
      pushToast(String(err), 'error');
    }
  }, [pushToast]);

  // Reload service list on mount and after canvas operations.
  useEffect(() => {
    void refreshServices();
  }, [refreshServices, canvasRevision]);

  // Load policies for the selected service.
  useEffect(() => {
    if (!selectedService) {
      setPolicies([]);
      setModules([]);
      return;
    }
    setLoading(true);
    getService(selectedService)
      .then((svc: ServiceConfig) => {
        setPolicies(svc.policy.map((p) => p.name));
        // Modules come from the same response the policies do; a module is a
        // service-level asset, so this is where it lives.
        setModules((svc.module ?? []).map((m) => m.name));
      })
      .catch((err: unknown) => {
        pushToast(String(err), 'error');
        setPolicies([]);
        setModules([]);
      })
      .finally(() => setLoading(false));
  }, [selectedService, pushToast, canvasRevision]);

  async function handleCreateService() {
    const name = newSvcName.trim();
    if (!name) return;
    try {
      const svc: ServiceConfig = {
        service_name: name,
        module: [],
        policy: [],
      };
      const res = await createService(svc);
      pushToast(res.msg ?? 'Service created', 'success');
      setNewSvcName('');
      setShowNewSvc(false);
      await refreshServices();
      selectService(name);
    } catch (err) {
      pushToast(String(err), 'error');
    }
  }

  async function handleUploadConfig(file: File) {
    setUploading(true);
    try {
      let res;
      try {
        res = await uploadServiceConfig(file, 'create');
      } catch (err) {
        // Same name already registered: offer the destructive reconfigure
        // path, which replaces the whole stored config.
        if (!String(err).includes('already exists')) throw err;
        const replace = confirm(
          'A service with this name already exists.\n\n' +
            'Replace its entire config with the uploaded file? Policies not in ' +
            'the file will be removed. The service must have no deployed policies.',
        );
        if (!replace) return;
        res = await uploadServiceConfig(file, 'reconfigure');
      }
      pushToast(res.msg ?? 'Config uploaded', 'success');
      bumpCanvasRevision();
      // Order matters: selectService clears the policy selection.
      selectService(res.service_name);
      const policies = res.policies ?? [];
      if (policies.length > 0) selectPolicy(policies[0]);
    } catch (err) {
      pushToast(String(err), 'error');
    } finally {
      setUploading(false);
    }
  }

  async function handleDeleteService(name: string) {
    if (!confirm(`Delete service "${name}"?`)) return;
    try {
      await deleteService(name);
      pushToast(`Service "${name}" deleted`, 'success');
      if (selectedService === name) selectService(null);
      await refreshServices();
    } catch (err) {
      pushToast(String(err), 'error');
    }
  }

  async function handleDeletePolicy(polName: string) {
    if (!selectedService) return;
    if (!confirm(`Delete policy "${polName}"?`)) return;
    try {
      await deletePolicy(selectedService, polName);
      pushToast(`Policy "${polName}" deleted`, 'success');
      if (selectedPolicy === polName) selectPolicy(null);
      bumpCanvasRevision();
    } catch (err) {
      pushToast(String(err), 'error');
    }
  }

  function handleNewPolicy() {
    const name = newPolName.trim();
    if (!name || !selectedService) return;
    // Open a blank canvas for the new policy name; the user saves it.
    selectPolicy(name, 'UNDEPLOYED');
    setNewPolName('');
    setShowNewPol(false);
  }

  function handleNewModule() {
    const name = newModName.trim();
    if (!name || !selectedService) return;
    // Same as a new policy: the blank canvas is the draft and Save creates it.
    selectModule(name);
    setNewModName('');
    setShowNewMod(false);
  }

  async function handleDeleteModule(modName: string) {
    if (!selectedService) return;
    if (!confirm(`Delete module "${modName}"?`)) return;
    try {
      await deleteModule(selectedService, modName);
      pushToast(`Module "${modName}" deleted`, 'success');
      if (selectedModule === modName) selectModule(null);
      bumpCanvasRevision();
    } catch (err) {
      // The server refuses while a policy still uses it, and the message names
      // which one, so it is worth surfacing verbatim.
      pushToast(String(err), 'error');
    }
  }

  return (
    <div className="service-panel">
      <div className="panel-section">
        <div className="panel-section-header">
          <span>Services</span>
          <span className="header-actions">
            <button
              className="icon-btn"
              title="Upload service config (YAML)"
              disabled={uploading}
              onClick={() => uploadInputRef.current?.click()}
            >
              ↥
            </button>
            <button className="icon-btn" title="New service" onClick={() => setShowNewSvc(!showNewSvc)}>
              +
            </button>
          </span>
          <input
            ref={uploadInputRef}
            type="file"
            accept=".yaml,.yml"
            style={{ display: 'none' }}
            onChange={(e) => {
              const file = e.target.files?.[0];
              // Clear immediately so re-selecting the same file fires again.
              e.target.value = '';
              if (file) void handleUploadConfig(file);
            }}
          />
        </div>

        {showNewSvc && (
          <div className="inline-form">
            <input
              value={newSvcName}
              onChange={(e) => setNewSvcName(e.target.value)}
              placeholder="Service name"
              onKeyDown={(e) => e.key === 'Enter' && void handleCreateService()}
              autoFocus
            />
            <button onClick={() => void handleCreateService()}>Create</button>
          </div>
        )}

        <ul className="item-list">
          {services.map((s) => (
            <li
              key={s}
              className={`item-list-row ${selectedService === s ? 'selected' : ''}`}
              onClick={() => selectService(s)}
            >
              <span className="item-name" title={s}>{s}</span>
              <button
                className="icon-btn danger"
                title="Delete service"
                onClick={(e) => {
                  e.stopPropagation();
                  void handleDeleteService(s);
                }}
              >
                ×
              </button>
            </li>
          ))}
          {services.length === 0 && (
            <li className="empty-hint">No services. Create one above.</li>
          )}
        </ul>
      </div>

      {selectedService && (
        <div className="panel-section">
          <div className="panel-section-header">
            <span>Policies — {selectedService}</span>
            <button className="icon-btn" title="New policy" onClick={() => setShowNewPol(!showNewPol)}>
              +
            </button>
          </div>

          {showNewPol && (
            <div className="inline-form">
              <input
                value={newPolName}
                onChange={(e) => setNewPolName(e.target.value)}
                placeholder="Policy name"
                onKeyDown={(e) => e.key === 'Enter' && handleNewPolicy()}
                autoFocus
              />
              <button onClick={handleNewPolicy}>New</button>
            </div>
          )}

          {loading ? (
            <div className="empty-hint">Loading…</div>
          ) : (
            <ul className="item-list">
              {policies.map((p) => (
                <li
                  key={p}
                  className={`item-list-row ${selectedPolicy === p ? 'selected' : ''}`}
                  onClick={() => selectPolicy(p)}
                >
                  <span className="item-name" title={p}>{p}</span>
                  <button
                    className="icon-btn danger"
                    title="Delete policy"
                    onClick={(ev) => {
                      ev.stopPropagation();
                      void handleDeletePolicy(p);
                    }}
                  >
                    ×
                  </button>
                </li>
              ))}
              {policies.length === 0 && (
                <li className="empty-hint">No policies yet.</li>
              )}
            </ul>
          )}
        </div>
      )}

      {selectedService && (
        <div className="panel-section">
          <div className="panel-section-header">
            <span>Modules — {selectedService}</span>
            <button className="icon-btn" title="New module" onClick={() => setShowNewMod(!showNewMod)}>
              +
            </button>
          </div>

          {showNewMod && (
            <div className="inline-form">
              <input
                value={newModName}
                onChange={(e) => setNewModName(e.target.value)}
                placeholder="Module name"
                onKeyDown={(e) => e.key === 'Enter' && handleNewModule()}
                autoFocus
              />
              <button onClick={handleNewModule}>New</button>
            </div>
          )}

          <ul className="item-list">
            {modules.map((m) => (
              <li
                key={m}
                className={`item-list-row ${selectedModule === m ? 'selected' : ''}`}
                onClick={() => selectModule(m)}
              >
                <span className="item-name" title={m}>{m}</span>
                <button
                  className="icon-btn danger"
                  title="Delete module"
                  onClick={(ev) => {
                    ev.stopPropagation();
                    void handleDeleteModule(m);
                  }}
                >
                  ×
                </button>
              </li>
            ))}
            {modules.length === 0 && (
              <li className="empty-hint">No modules yet.</li>
            )}
          </ul>
        </div>
      )}
    </div>
  );
}
