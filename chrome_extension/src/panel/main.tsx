// The panel: the React UI in the modal's iframe on github.com (embedded) or
// in its own popup window (`?standalone=1`). See App.tsx.

import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { App } from './App';
import { readPanelParams } from './params';

const params = readPanelParams(window.location.search);
document.documentElement.dataset.mode = params.standalone ? 'standalone' : 'embedded';

const root = document.getElementById('root');
if (root) {
    createRoot(root).render(
        <StrictMode>
            <App params={params} />
        </StrictMode>
    );
}
