import { createApp } from "vue";
import App from "./App.vue";
import "./style.css";
import { refreshCaps, refreshQueue, refreshState, startEventStream } from "./store";

refreshState();
refreshQueue();
refreshCaps();
startEventStream();

createApp(App).mount("#app");
