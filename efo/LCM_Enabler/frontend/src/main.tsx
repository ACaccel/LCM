import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import App from './App';
import './styles/global.css';

const rootEl = document.getElementById('root');
if (!rootEl) throw new Error('Root element #root not found in DOM');

createRoot(rootEl).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
