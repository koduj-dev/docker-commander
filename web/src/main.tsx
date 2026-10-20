import React from "react";
import ReactDOM from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { AuthProvider } from "./auth/AuthContext";
import { DialogProvider } from "./components/Dialog";
import { ToastProvider } from "./components/Toasts";
import App from "./App";
import "./index.css";
import { BASE_PATH } from "./lib/base";

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <BrowserRouter basename={BASE_PATH || undefined}>
      <AuthProvider>
        <DialogProvider>
          <ToastProvider>
            <App />
          </ToastProvider>
        </DialogProvider>
      </AuthProvider>
    </BrowserRouter>
  </React.StrictMode>
);
