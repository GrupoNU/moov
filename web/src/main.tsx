import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { MoovApp } from "./App";
import { registerServiceWorker } from "./pwa/register";

// Order matters: tokens define the custom properties that base.css consumes.
import "./styles/tokens.css";
import "./styles/base.css";

const container = document.getElementById("root");
if (container === null) {
  // A hard failure rather than a silent no-op: an index.html without #root is
  // a build problem, and a blank page with no error is the worst way to learn
  // about it.
  throw new Error("Moov: #root is missing from the document");
}

createRoot(container).render(
  <StrictMode>
    <MoovApp />
  </StrictMode>,
);

/*
 * E9: register the service worker.
 *
 * AFTER the render call and deliberately not awaited. Registration is a
 * network round trip and an install; blocking first paint on it would trade
 * the app's startup time for a capability that matters on the SECOND visit.
 * `registerServiceWorker` is feature-detected and never rejects, so the `void`
 * here is discarding a promise that has already handled its own failures — not
 * ignoring one.
 */
void registerServiceWorker();
