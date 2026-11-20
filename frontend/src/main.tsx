import React from "react";
import ReactDOM from "react-dom/client";
import { AppProvider } from "./state/app";
import { Shell } from "./App";
import "./styles.css";

ReactDOM.createRoot(document.getElementById("root") as HTMLElement).render(
  <React.StrictMode>
    <AppProvider>
      <Shell />
    </AppProvider>
  </React.StrictMode>,
);
