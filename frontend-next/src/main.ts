import { createApp } from "vue";
import App from "./App.vue";
import { setupTheme } from "./theme";
import { setupRouter } from "./router";
import "./styles/main.css";

setupTheme();
setupRouter();
createApp(App).mount("#app");
