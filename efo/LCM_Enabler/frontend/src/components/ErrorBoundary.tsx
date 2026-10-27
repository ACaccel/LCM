/**
 * ErrorBoundary.tsx
 *
 * Keeps a render failure inside one panel instead of blanking the editor.
 *
 * A canvas built from stored data reads values the server owns, and the whole
 * app used to unmount when any of them was not the shape the code assumed --
 * an entirely black window with the reason only in the browser console. Failing
 * in place is recoverable: the other panels still work, the message names the
 * cause, and Reset re-renders without a page reload.
 */

import { Component, type ErrorInfo, type ReactNode } from 'react';

type Props = {
  /** Names the failing area in the message, e.g. "canvas". */
  label: string;
  children: ReactNode;
};

type State = { error: Error | null };

export default class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo): void {
    // Kept: the stack is the only place the failing component chain appears.
    console.error(`${this.props.label} failed to render`, error, info.componentStack);
  }

  render(): ReactNode {
    const { error } = this.state;
    if (!error) return this.props.children;

    return (
      <div className="error-boundary">
        <div className="error-boundary-title">The {this.props.label} could not be drawn</div>
        <pre className="error-boundary-message">{error.message}</pre>
        <p className="error-boundary-hint">
          This usually means stored data is not the shape the editor expects. The other panels
          still work, so you can switch policy or reload.
        </p>
        <button className="btn-secondary" onClick={() => this.setState({ error: null })}>
          Try again
        </button>
      </div>
    );
  }
}
