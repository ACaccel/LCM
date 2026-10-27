/**
 * NodePalette.tsx
 *
 * Draggable node palette in the left panel.
 * Nodes can be added to the canvas either by dragging or by clicking (which
 * places them at the centre of the current viewport).
 * Node type data is fetched from GET /api/lcm/nodetypes and merged with
 * static TRIGGER_TYPES / FUNCTION_TYPES as a fallback.
 */

import { useEffect, useState } from 'react';
import { getNodeTypes } from '../api/lcmApi';
import { TRIGGER_TYPES, FUNCTION_TYPES } from '../model/nodePortInfo';
import { useNodeSchemas } from '../model/nodeSchemas';
import { useStore } from '../hooks/useStore';
import type { ModuleConfig, NodeSchema } from '../model/types';
import '../styles/NodePalette.css';

type PaletteCategory = {
  label: string;
  types: string[];
};

type Props = {
  /** Called when the user clicks a node type to add it to the canvas. */
  onAddNode: (nodeType: string) => void;
  /** Called when the user clicks a module to place an instance of it. */
  onAddModule: (moduleName: string) => void;
  /** The service's modules, offered as instantiable items. */
  modules: ModuleConfig[];
};

export default function NodePalette({ onAddNode, onAddModule, modules }: Props) {
  const { schemas } = useNodeSchemas();
  const selectedModule = useStore((s) => s.selectedModule);
  const [categories, setCategories] = useState<PaletteCategory[]>([
    { label: 'Triggers', types: [...TRIGGER_TYPES] },
    { label: 'Functions', types: [...FUNCTION_TYPES] },
  ]);

  useEffect(() => {
    getNodeTypes()
      .then((res) => {
        // Merge server-supplied types with the static list so any new types
        // registered server-side appear automatically.
        const serverTriggers = res.triggers.map((t) => t.type);
        const serverFns = res.functions.map((f) => f.type);

        const allTriggers = [...new Set([...TRIGGER_TYPES, ...serverTriggers])];
        const allFns = [...new Set([...FUNCTION_TYPES, ...serverFns])];

        setCategories([
          { label: 'Triggers', types: allTriggers },
          { label: 'Functions', types: allFns },
        ]);
      })
      .catch(() => {
        // If the backend is unreachable, fall back to the static list silently.
      });
  }, []);

  function handleDragStart(e: React.DragEvent<HTMLDivElement>, nodeType: string) {
    e.dataTransfer.setData('application/lcm-node-type', nodeType);
    e.dataTransfer.effectAllowed = 'copy';
  }

  function handleModuleDragStart(e: React.DragEvent<HTMLDivElement>, moduleName: string) {
    e.dataTransfer.setData('application/lcm-module-name', moduleName);
    e.dataTransfer.effectAllowed = 'copy';
  }

  // A module declares no triggers of its own. "Module" is never a bare item
  // either: which module an instance means decides its port count, so it is
  // added by name from the list below.
  const shown = categories.filter((cat) => !(selectedModule && cat.label === 'Triggers'));

  return (
    <div className="node-palette">
      <div className="palette-title">Node Palette</div>
      {shown.map((cat) => (
        <div key={cat.label} className="palette-category">
          <div className="palette-category-label">{cat.label}</div>
          {cat.types.map((t) => (
            <div
              key={t}
              className="palette-item"
              draggable
              onDragStart={(e) => handleDragStart(e, t)}
              onClick={() => onAddNode(t)}
              title={describe(t, schemas.get(t))}
            >
              {t}
            </div>
          ))}
        </div>
      ))}
      {modules.length > 0 && (
        <div className="palette-category">
          <div className="palette-category-label">Modules</div>
          {modules.map((m) => (
            <div
              key={m.name}
              className="palette-item module"
              draggable
              onDragStart={(e) => handleModuleDragStart(e, m.name)}
              onClick={() => onAddModule(m.name)}
              title={`${m.numInPort} in / ${m.numOutPort} out — drag or click to place an instance`}
            >
              {m.name}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

/** Tooltip summarising what a node consumes and produces. */
function describe(nodeType: string, schema: NodeSchema | undefined): string {
  if (!schema) return `Drag or click to add ${nodeType}`;

  const lines = [`${nodeType} — ${schema.numInputPort} in / ${schema.numOutputPort} out`];
  if (schema.input === null) {
    lines.push('reads no input; configured entirely by its properties');
  } else if (schema.input.fields.length > 0) {
    lines.push(`input: ${schema.input.fields.map((f) => f.name).join(', ')}`);
  }
  if (schema.output?.dynamic) {
    lines.push('output: whatever the caller sends');
  } else if (schema.output && schema.output.fields.length > 0) {
    lines.push(`output: ${schema.output.fields.map((f) => f.name).join(', ')}`);
  }
  return lines.join('\n');
}
