import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { browserTransport } from "./api/transport";
import { App } from "./app/App";

const root = document.getElementById("root");
if (root === null) {
  throw new Error("index.html has no #root element");
}
createRoot(root).render(
  <StrictMode>
    <App transport={browserTransport()} />
  </StrictMode>,
);
