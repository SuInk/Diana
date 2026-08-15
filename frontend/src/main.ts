import { createApp } from "vue";
import App from "./App.vue";
import LandingPage from "./LandingPage.vue";
import "./styles.css";

const consolePaths = new Set(["/console", "/admin", "/webui", "/llm", "/test", "/qqbot", "/groups", "/plugins", "/logs", "/theme"]);
const currentPath = window.location.pathname.replace(/\/+$/, "") || "/";
const rootComponent = consolePaths.has(currentPath) ? App : LandingPage;

createApp(rootComponent).mount("#app");
