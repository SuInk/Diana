import { createApp } from "vue";
import App from "./App.vue";
import { setupTheme } from "./theme";
import { setupRouter } from "./router";
import "./styles/main.css";

async function bootstrap(): Promise<void> {
  if (import.meta.env.VITE_DEMO_MODE === "true") {
    const { installDemoMode } = await import("./demo");
    installDemoMode();
  }
  setupTheme();
  setupRouter();
  createApp(App).mount("#app");
}

void bootstrap();
