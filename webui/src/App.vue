<script setup lang="ts">
import MiniPlayer from "./components/MiniPlayer.vue";
import NowPlayingHero from "./components/NowPlayingHero.vue";
import PlayNextList from "./components/PlayNextList.vue";
import QueueList from "./components/QueueList.vue";
import StatusPill from "./components/StatusPill.vue";
import Toasts from "./components/Toasts.vue";
import { store, setView } from "./store";
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

    <template v-if="store.view === 'nowplaying'">
      <NowPlayingHero />
    </template>
    <template v-else-if="store.view === 'queue'">
      <PlayNextList />
      <QueueList />
    </template>
    <template v-else>
      <p class="muted">browse lands in a moment…</p>
    </template>

    <MiniPlayer v-if="store.view !== 'nowplaying'" />

    <nav class="tabs">
      <button :class="{ on: store.view === 'nowplaying' }" @click="setView('nowplaying')">Now Playing</button>
      <button :class="{ on: store.view === 'queue' }" @click="setView('queue')">Queue</button>
      <button :class="{ on: store.view === 'browse' }" @click="setView('browse')">Browse</button>
    </nav>

    <Toasts />
  </main>
</template>

<style scoped>
.tabs {
  position: sticky;
  bottom: 0;
  display: flex;
  gap: 4px;
  padding: 6px 0;
  background: var(--surface);
}
.tabs button {
  flex: 1;
  padding: 10px 0;
  border-radius: 10px;
}
.tabs button.on {
  border-color: var(--accent);
  color: var(--accent);
}
.muted {
  color: var(--muted);
}
</style>
