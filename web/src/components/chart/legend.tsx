export interface LegendItem {
  name: string;
  color: string;
  dashed?: boolean;
  /** Draw a filled square instead of a line, for bars. */
  swatch?: boolean;
}

/**
 * Names the series of a chart. Lines differ by dash as well as colour and
 * bars are named here, so identity does not rest on colour alone.
 */
export function ChartKey({ items }: { items: readonly LegendItem[] }) {
  return (
    <div className="chart-legend">
      {items.map((i) => (
        <span key={i.name}>
          <i className={`key ${i.swatch ? "bar" : i.dashed ? "series-dashed" : "series"}`} style={{ ["--key" as string]: i.color }} aria-hidden="true" />
          {i.name}
        </span>
      ))}
    </div>
  );
}
