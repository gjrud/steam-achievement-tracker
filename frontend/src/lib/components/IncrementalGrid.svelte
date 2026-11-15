<script context="module">
  // Cards mounted first, and cards added per growth step.
  export const INITIAL = 48
  export const STEP = 48
</script>

<script>
  import {onDestroy, tick} from 'svelte'

  // items is the full filtered list. Only the {#each} below slices it; counts elsewhere use the full list.
  export let items = []
  // Changing resetKey (search text, tag filter) shows INITIAL cards again. Plain state refreshes keep the limit.
  export let resetKey = ''
  // Class for the wrapper div that holds the cards (the grid class), so layout matches the old markup.
  export let gridClass = ''

  // Card markup stays with the parent: the parent fills the default slot with `let:game`.
  let limit = INITIAL
  let sentinel = null
  let wrapper = null
  let watched = null
  let destroyed = false
  // Null in environments without IntersectionObserver (jsdom); the button still works there.
  const observer = typeof IntersectionObserver === 'undefined'
    ? null
    : new IntersectionObserver(onIntersect, {rootMargin: '1500px'})

  $: resetKey, (limit = INITIAL)
  $: shown = Math.min(limit, items.length)
  $: hasMore = items.length > limit
  $: watch(hasMore ? sentinel : null)

  function watch(el) {
    if (!observer || el === watched) return
    if (watched) observer.unobserve(watched)
    watched = el
    if (el) observer.observe(el)
  }

  function onIntersect(entries) {
    if (entries.some((entry) => entry.isIntersecting)) grow()
  }

  // grow runs for observer hits and button clicks.
  async function grow() {
    if (limit >= items.length) return
    // Capture focus before the button is removed. Focus moves only when the button itself had it.
    const restoreFocus = sentinel !== null && document.activeElement === sentinel
    const previousLimit = limit
    limit += STEP
    await tick()
    if (destroyed) return
    // IntersectionObserver only reports changes. Re-observing makes it report the current position,
    // so growth continues while the button stays near the viewport.
    if (observer && watched) {
      observer.unobserve(watched)
      observer.observe(watched)
    }
    // The focused button is gone once the list is complete, so put focus on the first card it revealed.
    if (restoreFocus && limit >= items.length) focusCard(previousLimit)
  }

  // Each slot renders exactly one root element (GameCard's <article>), so child index equals item index.
  function focusCard(index) {
    const card = wrapper?.children[index]
    if (!card) return
    if (!card.hasAttribute('tabindex')) card.setAttribute('tabindex', '-1')
    card.focus()
  }

  onDestroy(() => {
    destroyed = true
    observer?.disconnect()
    watched = null
  })
</script>

<div class={gridClass} bind:this={wrapper}>{#each items.slice(0, limit) as game (game.entryKey)}<slot {game} />{/each}</div>
{#if hasMore}<div class="grid-more"><button bind:this={sentinel} type="button" class="secondary" on:click={grow}>Show more ({shown} of {items.length})</button></div>{/if}
