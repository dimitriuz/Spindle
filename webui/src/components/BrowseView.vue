<script setup lang="ts">
import { ref, onMounted, onUnmounted } from "vue";
import {
  store,
  loadProviders,
  pickProvider,
  pickTab,
  tabsFor,
  openDrill,
  closeDrill,
  retryBrowse,
  browseMore,
  browseSearch,
  playTrack,
  queueTrack,
  loadPlaylist,
  playAlbumDrill,
} from "../store";

onMounted(loadProviders);

const sentinel = ref<HTMLElement | null>(null);
const io = new IntersectionObserver((entries) => {
  if (entries.some((e) => e.isIntersecting)) browseMore();
});
// the sentinel is re-rendered whenever the list changes; re-observing every
// 250 ms is the boring version and fine for one 1px element.
let poll = 0;
onMounted(() => {
  poll = window.setInterval(() => {
    if (sentinel.value) io.observe(sentinel.value);
  }, 250);
});
onUnmounted(() => {
  window.clearInterval(poll);
  io.disconnect();
});

const providerInfo = () =>
  store.browse.providers.find((p) => p.key === store.browse.provider) ??
  store.browse.providers[0];

function searchValue(ev: Event): string {
  const t = ev.target;
  if (t !== null && "value" in t) return String(t.value);
  return "";
}
</script>

<template>
  <section class="browse" v-if="store.browse.providers.length">
    <div class="chips">
      <button
        v-for="p in store.browse.providers"
        :key="p.key"
        :class="{ on: p.key === store.browse.provider }"
        @click="pickProvider(p.key)"
      >
        {{ p.name }}
      </button>
    </div>

    <div class="subtabs" v-if="!store.browse.stack.length">
      <button
        v-for="t in tabsFor(providerInfo())"
        :key="t"
        :class="{ on: t === store.browse.tab }"
        @click="pickTab(t)"
      >
        {{ t }}
      </button>
    </div>

    <p v-if="store.browse.stack.length" class="crumbs">
      <button class="back" @click="closeDrill()">← back</button>
      {{ store.browse.stack.map((s) => s.title).join(" / ") }}
    </p>

    <p v-if="store.browse.error" class="banner">
      {{ store.browse.error }}
      <button class="retry" @click="retryBrowse()">retry</button>
    </p>

    <p v-if="store.browse.stack.length && store.browse.stack[store.browse.stack.length - 1].kind === 'album'" class="albumhead">
      <button class="act" aria-label="play album" @click.stop="playAlbumDrill()">play album</button>
    </p>

    <ul class="rows">
      <li
        v-for="(row, i) in store.browse.list"
        :key="`${row.id ?? ''}|${String(row.title ?? '')}|${i}`"
        class="row"
      >
        <template v-if="store.browse.tab === 'albums' && !store.browse.stack.length">
          <div class="drill" @click="openDrill({ kind: 'album', id: row.id ?? '', title: String(row.name ?? '') })">
            <b>{{ row.name }}</b>
            <span class="muted">{{ String(row.artist ?? "") }} · {{ String(row.year ?? "") }}</span>
          </div>
        </template>
        <template v-else-if="store.browse.tab === 'artists' && !store.browse.stack.length">
          <div class="drill" @click="openDrill({ kind: 'artist', id: row.id ?? '', title: String(row.name ?? '') })">
            <b>{{ row.name }}</b>
          </div>
        </template>
        <template v-else-if="(store.browse.tab === 'playlists' || store.browse.tab === 'catalog') && !store.browse.stack.length">
          <b>{{ row.name }}</b>
          <span class="muted">{{ row.track_count ?? "" }}</span>
          <button v-if="row.id" class="act" aria-label="load playlist" @click.stop="loadPlaylist(row.id ?? '', row.name ?? '')">load</button>
        </template>
        <template v-else>
          <span class="t">{{ row.title }}</span>
          <span class="muted">{{ row.artist ? "· " + String(row.artist) : "" }}</span>
          <button class="act" aria-label="play now" @click.stop="playTrack(row)">▶</button>
          <button class="act" aria-label="queue next" @click.stop="queueTrack(row)">+</button>
        </template>
      </li>
    </ul>
    <div v-if="store.browse.tab === 'search' && !store.browse.stack.length" class="searchbar">
      <input class="searchbox" type="text" placeholder="search this provider…" maxlength="80" :value="store.browse.query" @input="browseSearch(searchValue($event))" />
      <span class="muted">{{ store.browse.query }}</span>
    </div>

    <p
      v-if="!store.browse.loading && !store.browse.error && store.browse.list.length === 0"
      class="muted"
    >
      no results
    </p>

    <div ref="sentinel" class="sentinel"></div>
  </section>
</template>

<style scoped>
.chips {
  display: flex;
  flex-wrap: wrap;
  gap: 0.4rem;
  margin-bottom: 0.5rem;
}
.chips button {
  border-radius: 999px;
  padding: 0.15rem 0.7rem;
  font-size: 0.85rem;
}
.chips button.on {
  border-color: var(--accent);
  color: var(--accent);
}
.subtabs {
  display: flex;
  gap: 0.4rem;
  margin-bottom: 0.4rem;
}
.subtabs button {
  font-size: 0.8rem;
  padding: 0.1rem 0.55rem;
  border-radius: 6px;
}
.subtabs button.on {
  border-color: var(--accent);
  color: var(--accent);
}
.crumbs {
  color: var(--dim);
  font-size: 0.85rem;
  margin-bottom: 0.4rem;
}
.crumbs .back {
  font: inherit;
  color: var(--accent);
}
.banner {
  color: var(--error);
  margin-bottom: 0.4rem;
}
.banner .retry {
  font: inherit;
  color: var(--accent);
}
.rows {
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 0.3rem;
}
.row {
  display: flex;
  align-items: baseline;
  gap: 0.5rem;
  border-radius: 8px;
  padding: 0.35rem 0.5rem;
}
.row:nth-child(odd) { background: #101216; }
.drill {
  display: flex;
  align-items: baseline;
  gap: 0.5rem;
  flex: 1;
  min-width: 0;
  cursor: pointer;
  color: var(--fg);
}
.drill b { flex: 1; min-width: 0; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.t { flex: 1; min-width: 0; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; color: var(--fg); }
.muted { color: var(--muted); font-size: 0.8rem; flex: none; max-width: 42%; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.sentinel {
  height: 1px;
}
.act {
  flex: none;
  font-size: 0.75rem;
  padding: 0.05rem 0.5rem;
  border-radius: 5px;
  color: var(--accent);
}
.searchbar {
  display: flex;
  align-items: center;
  gap: 0.4rem;
  margin-bottom: 0.4rem;
}
.searchbox {
  flex: 1;
  min-width: 14rem;
  background: var(--surface);
  color: var(--fg);
}
.albumhead {
  color: var(--dim);
  font-size: 0.85rem;
  margin-bottom: 0.3rem;
}
</style>
