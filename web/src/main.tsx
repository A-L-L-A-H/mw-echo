import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { App } from './App';
import './index.css';

const rootContainer = document.getElementById('root');
if (!rootContainer) {
  throw new Error('Root mount element #root not found');
}

createRoot(rootContainer).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
