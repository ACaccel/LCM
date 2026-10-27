/**
 * useServiceModules.ts
 *
 * The open service's modules.
 *
 * They are fetched separately from the node catalog because a module is not a
 * node type: how many ports an instance has is declared per service, so no
 * published schema can describe it, and both the palette and the canvas need
 * that arity before they can place or draw one.
 */

import { useEffect, useState } from 'react';
import { getService } from '../api/lcmApi';
import { useStore } from './useStore';
import type { ModuleConfig } from '../model/types';

export function useServiceModules(): ModuleConfig[] {
  const selectedService = useStore((s) => s.selectedService);
  const canvasRevision = useStore((s) => s.canvasRevision);
  const [modules, setModules] = useState<ModuleConfig[]>([]);

  useEffect(() => {
    if (!selectedService) {
      setModules([]);
      return;
    }
    let current = true;
    void getService(selectedService)
      .then((svc) => {
        // A stale response must not overwrite a newer one after the user has
        // already switched services.
        if (current) setModules(svc.module ?? []);
      })
      // Losing the module list must not block editing; it only costs the
      // palette its Modules section until the next reload.
      .catch(() => {
        if (current) setModules([]);
      });
    return () => {
      current = false;
    };
  }, [selectedService, canvasRevision]);

  return modules;
}
