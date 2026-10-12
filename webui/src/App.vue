<script setup lang="ts">
import NowPlaying from "./components/NowPlaying.vue";
import PlayNextList from "./components/PlayNextList.vue";
import QueueList from "./components/QueueList.vue";
import StatusPill from "./components/StatusPill.vue";
import Toasts from "./components/Toasts.vue";
import TransportBar from "./components/TransportBar.vue";
import { store } from "./store";
</script>

<template>
  <main class="wrap">
    <header class="bar">
      <span class="brand">cliamp</span>
      <StatusPill />
    </header>

    <p v-if="!store.online" class="banner">
      player offline — controls paused, state may be stale
    </p>

    <NowPlaying />
    <TransportBar />
    <PlayNextList />
    <QueueList />

    <Toasts />
  </main>
</template>
