<script setup lang="ts">
import NowPlaying from "./components/NowPlaying.vue";
import PlayNextList from "./components/PlayNextList.vue";
import QueueList from "./components/QueueList.vue";
import TransportBar from "./components/TransportBar.vue";
import { store } from "./store";
</script>

<template>
  <main class="wrap">
    <header class="bar">
      <span class="brand">cliamp</span>
      <span :class="['pill', store.online ? 'on' : 'off']">
        {{ store.online ? "online" : "offline" }}
      </span>
    </header>
    <NowPlaying />
    <TransportBar />
    <PlayNextList />
    <QueueList />
  </main>
</template>
