<script setup lang="ts">
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

    <section class="now">
      <h1>{{ store.snapshot.track?.title ?? "—" }}</h1>
      <p>{{ store.snapshot.track?.artist ?? "" }}</p>
    </section>
  </main>
</template>
