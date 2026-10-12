import { createApp } from "vue";
import App from "./App.vue";
import "./style.css";
import { connectEvents, refreshCaps, refreshQueue, refreshState } from "./store";

refreshState();
refreshQueue();
refreshCaps();
connectEvents();

createApp(App).mount("#app");
