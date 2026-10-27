/**
 * SchemaPropertyForm.tsx
 *
 * Renders a node's properties from the schema the backend publishes, so the
 * field names, their types and their legal values never have to be typed from
 * memory or hard-coded here.
 *
 * Two things this fixes beyond convenience:
 *  - A request value outside the node's closed set falls through to an
 *    unsupported-request 400 with no other signal, so those become dropdowns.
 *  - Values are committed in the type the field declares. Storing a []string as
 *    "a,b" makes the property decode fail, and since no node checks that error
 *    the node silently does nothing at all.
 */

import { useState } from 'react';
import { coerce, toControlValue, toRowList, toStringList } from '../model/coerce';
import { extraPropertyKeys, strandedProperties, visibleProperties } from '../model/nodeSchemas';
import type { FieldSchema, NodeSchema } from '../model/types';

type Props = {
  schema: NodeSchema;
  property: Record<string, unknown>;
  onChange: (key: string, value: unknown) => void;
  /**
   * Field names reachable on one input port, for a property that names a field
   * rather than holding a value. Absent outside a graph context, in which case
   * those properties stay plain text.
   */
  fieldChoices?: (port: number) => string[];
};

export default function SchemaPropertyForm({
  schema,
  property,
  onChange,
  fieldChoices,
}: Props): JSX.Element {
  const [showStranded, setShowStranded] = useState(false);
  const [revealed, setRevealed] = useState<Record<string, boolean>>({});

  const visible = visibleProperties(schema, property);
  const stranded = strandedProperties(schema, property);
  const extras = extraPropertyKeys(schema, property);

  const discriminator = schema.discriminator;
  const discriminatorSet =
    !discriminator || String(property[discriminator] ?? '').trim() !== '';

  return (
    <>
      {visible.map((field) => (
        <PropertyControl
          key={field.name}
          field={field}
          value={property[field.name]}
          revealed={revealed[field.name] ?? false}
          onReveal={() => setRevealed((r) => ({ ...r, [field.name]: !r[field.name] }))}
          onChange={(v) => onChange(field.name, v)}
          fieldChoices={fieldChoices}
        />
      ))}

      {!discriminatorSet && (
        <div className="inspector-sub-label">
          Choose a {discriminator} to see the properties it needs.
        </div>
      )}

      {stranded.length > 0 && (
        <div className="prop-group">
          <button className="btn-secondary" onClick={() => setShowStranded((v) => !v)}>
            {showStranded ? 'Hide' : 'Show'} {stranded.length} value
            {stranded.length === 1 ? '' : 's'} not used by this{' '}
            {discriminator ?? 'request'}
          </button>
          {showStranded &&
            stranded.map((field) => (
              <PropertyControl
                key={field.name}
                field={field}
                value={property[field.name]}
                revealed={revealed[field.name] ?? false}
                onReveal={() => setRevealed((r) => ({ ...r, [field.name]: !r[field.name] }))}
                onChange={(v) => onChange(field.name, v)}
                fieldChoices={fieldChoices}
              />
            ))}
        </div>
      )}

      {extras.length > 0 && (
        <div className="prop-group">
          <div className="inspector-sub-label">
            Extra properties (not in this node&apos;s schema)
          </div>
          {extras.map((key) => (
            <div key={key} className="prop-group">
              <label>{key}</label>
              <input
                value={toControlValue(property[key])}
                onChange={(e) => onChange(key, e.target.value)}
              />
            </div>
          ))}
        </div>
      )}
    </>
  );
}

type ControlProps = {
  field: FieldSchema;
  value: unknown;
  revealed: boolean;
  onReveal: () => void;
  onChange: (value: unknown) => void;
  fieldChoices?: (port: number) => string[];
};

function PropertyControl({
  field,
  value,
  revealed,
  onReveal,
  onChange,
  fieldChoices,
}: ControlProps): JSX.Element {
  const label = (
    <label>
      {field.name}
      {field.required && <span className="prop-required"> *</span>}
    </label>
  );

  if (field.enum && field.enum.length > 0) {
    const current = toControlValue(value);
    // A stored value outside the list would render as a blank select without
    // firing onChange, so it would look unset while silently persisting.
    const unknownValue = current !== '' && !field.enum.includes(current);
    return (
      <div className="prop-group">
        {label}
        <select value={current} onChange={(e) => onChange(coerce(field.type, e.target.value))}>
          <option value="">— choose —</option>
          {field.enum.map((v) => (
            <option key={v} value={v}>
              {v}
            </option>
          ))}
          {unknownValue && <option value={current}>⚠ {current} (not a known value)</option>}
        </select>
        {field.doc && <div className="inspector-sub-label">{field.doc}</div>}
      </div>
    );
  }

  if (field.type === 'object[]') {
    return (
      <RowListControl
        field={field}
        value={value}
        label={label}
        onChange={onChange}
        fieldChoices={fieldChoices}
      />
    );
  }

  if (field.type === 'string[]') {
    return (
      <StringListControl field={field} value={value} label={label} onChange={onChange} />
    );
  }

  if (field.type === 'boolean') {
    return (
      <div className="prop-group">
        {label}
        <input
          type="checkbox"
          checked={value === true}
          onChange={(e) => onChange(e.target.checked)}
        />
        {field.doc && <div className="inspector-sub-label">{field.doc}</div>}
      </div>
    );
  }

  if (field.secret) {
    return (
      <div className="prop-group">
        {label}
        <div className="tf-row">
          <input
            type={revealed ? 'text' : 'password'}
            value={toControlValue(value)}
            onChange={(e) => onChange(coerce(field.type, e.target.value))}
          />
          <button className="icon-btn" onClick={onReveal} title="Show or hide">
            {revealed ? '🙈' : '👁'}
          </button>
        </div>
        <div className="inspector-sub-label">
          Display mask only — this is stored and returned in cleartext.
        </div>
      </div>
    );
  }

  const numeric = field.type === 'integer' || field.type === 'number';
  return (
    <div className="prop-group">
      {label}
      <input
        type={numeric ? 'number' : 'text'}
        value={toControlValue(value)}
        onChange={(e) => onChange(coerce(field.type, e.target.value))}
      />
      {field.filePath && (
        <div className="inspector-sub-label">Relative to the service&apos;s upload directory.</div>
      )}
      {field.doc && <div className="inspector-sub-label">{field.doc}</div>}
    </div>
  );
}

type ListProps = {
  field: FieldSchema;
  value: unknown;
  label: JSX.Element;
  onChange: (value: unknown) => void;
  fieldChoices?: (port: number) => string[];
};

/** Repeatable text rows; commits a real array, never a joined string. */
function StringListControl({ field, value, label, onChange }: ListProps): JSX.Element {
  const items = toStringList(value);

  function commit(next: string[]) {
    onChange(coerce('string[]', next));
  }

  return (
    <div className="prop-group">
      {label}
      {items.map((item, idx) => (
        <div key={idx} className="tf-row">
          <input
            value={item}
            onChange={(e) => commit(items.map((v, i) => (i === idx ? e.target.value : v)))}
          />
          <button
            className="icon-btn danger"
            onClick={() => commit(items.filter((_, i) => i !== idx))}
          >
            ×
          </button>
        </div>
      ))}
      <button className="btn-secondary" onClick={() => commit([...items, ''])}>
        + Add {field.name} entry
      </button>
    </div>
  );
}

/**
 * Repeatable rows for an object[] property, such as If's condition list.
 *
 * The schema carries the element's fields, so this stays generic: nothing here
 * knows what a condition is. Without it a nested property falls through to the
 * raw key/value editor, where the only way to enter a list of records is to type
 * JSON by hand.
 */
function RowListControl({ field, value, label, onChange, fieldChoices }: ListProps): JSX.Element {
  const rows = toRowList(value);
  const columns = field.item ?? [];

  // Undefined rather than [] for an empty list, matching coerce: an absent key
  // reads as "not configured" to both the backend and the required check.
  function commit(next: Record<string, unknown>[]) {
    onChange(next.length > 0 ? next : undefined);
  }

  function setCell(index: number, key: string, cell: unknown) {
    commit(
      rows.map((row, i) => {
        if (i !== index) return row;
        const updated = { ...row };
        if (cell === undefined) delete updated[key];
        else updated[key] = cell;
        return updated;
      }),
    );
  }

  if (columns.length === 0) {
    return (
      <div className="prop-group">
        {label}
        <div className="inspector-sub-label">
          The server published no element fields for this property, so it cannot be edited here.
        </div>
      </div>
    );
  }

  return (
    <div className="prop-group">
      {label}
      {field.doc && <div className="inspector-sub-label">{field.doc}</div>}

      {rows.length === 0 && (
        <div className="inspector-sub-label">None yet — add one to configure this node.</div>
      )}

      {rows.map((row, index) => (
        <div key={index} className="row-item">
          <div className="row-item-head">
            <span className="row-item-index">{index + 1}</span>
            <button
              className="icon-btn danger"
              title="Remove this row"
              onClick={() => commit(rows.filter((_, i) => i !== index))}
            >
              ×
            </button>
          </div>
          <div className="row-item-cells">
            {columns.map((column) => (
              <RowCell
                key={column.name}
                field={column}
                value={row[column.name]}
                choices={
                  column.fieldRef ? (fieldChoices?.(column.fieldRefPort ?? 0) ?? []) : undefined
                }
                onChange={(cell) => setCell(index, column.name, cell)}
              />
            ))}
          </div>
        </div>
      ))}

      <button className="btn-secondary" onClick={() => commit([...rows, {}])}>
        + Add {field.name.replace(/s$/, '')}
      </button>
    </div>
  );
}

type CellProps = {
  field: FieldSchema;
  value: unknown;
  /** Suggested field names when this cell names a payload field. */
  choices?: string[];
  onChange: (value: unknown) => void;
};

/** One cell inside a row. Deliberately flat: item fields never nest further. */
function RowCell({ field, value, choices, onChange }: CellProps): JSX.Element {
  const current = toControlValue(value);
  const title = field.doc ? `${field.name} — ${field.doc}` : field.name;

  // A field reference is suggested, never restricted: a webhook payload's shape
  // is the caller's to decide and is only known after a run, so a name that has
  // not been seen yet still has to be typeable.
  if (choices) {
    const listId = `fields-${field.name}-${field.fieldRefPort ?? 0}`;
    return (
      <label className="row-cell" title={title}>
        <span>{field.name}</span>
        <input
          type="text"
          list={listId}
          value={current}
          placeholder={choices.length > 0 ? 'field or node.field' : 'field name'}
          onChange={(e) => onChange(e.target.value)}
        />
        <datalist id={listId}>
          {choices.map((name) => (
            <option key={name} value={name} />
          ))}
        </datalist>
      </label>
    );
  }

  if (field.enum && field.enum.length > 0) {
    const unknownValue = current !== '' && !field.enum.includes(current);
    return (
      <label className="row-cell" title={title}>
        <span>{field.name}</span>
        <select value={current} onChange={(e) => onChange(coerce(field.type, e.target.value))}>
          <option value="">—</option>
          {field.enum.map((v) => (
            <option key={v} value={v}>
              {v}
            </option>
          ))}
          {unknownValue && <option value={current}>⚠ {current}</option>}
        </select>
      </label>
    );
  }

  const numeric = field.type === 'integer' || field.type === 'number';
  return (
    <label className="row-cell" title={title}>
      <span>{field.name}</span>
      <input
        type={numeric ? 'number' : 'text'}
        value={current}
        onChange={(e) => onChange(coerce(field.type, e.target.value))}
      />
    </label>
  );
}
