import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { MoovApp } from "./App";

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
