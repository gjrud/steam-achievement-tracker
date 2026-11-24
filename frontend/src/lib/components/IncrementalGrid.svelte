<script module>
  // Cards mounted first, and cards added per growth step.
  export const INITIAL = 48
  export const STEP = 48
  // Growth starts this many px before the list reaches the viewport.
  const ROOT_MARGIN_PX = 1500
  // Measured on WebKitGTK: 0.99 keeps the estimate within about ±1%. Underestimating is safe (the page grows, no scroll clamp); 0.97 overcorrected.
  export const SPACER_BIAS = 0.99
</script>

<script>
  import {onDestroy, onMount, tick} from 'svelte'

  // items is the full filtered list. Only the {#each} below slices it; counts elsewhere use the full list.
  // children: renders one card per visible item.
  let {
    items = [],
    // Changing resetKey (search text, tag filter) shows INITIAL cards again. Plain state refreshes keep the limit.
    resetKey = '',
    // Class for the wrapper div that holds the cards (the grid class), so layout matches the old markup.
    gridClass = '',
    children
  } = $props()

  let limit = $state(INITIAL)
  // $state: bound inside {#if hasMore}, so a plain let trips non_reactive_update; the watch effect also reruns when moreBox binds.
  let sentinel = $state(null)
  let moreBox = $state(null)
  let wrapper = null
  let watched = null
  let destroyed = false
  // Last layout measurement. pitch is 0 when there is no usable layout (jsdom, hidden grid).
  let cols = 1
  let pitch = 0
  // Blank space under the button that stands in for the rows not mounted yet, so the document keeps its full height.
  let spacerHeight = $state(0)
  // Null in environments without IntersectionObserver (jsdom); the button still works there.
  const observer = typeof IntersectionObserver === 'undefined'
    ? null
    : new IntersectionObserver(onIntersect, {rootMargin: `${ROOT_MARGIN_PX}px`})
  let resizeObserver = null

  let shown = $derived(Math.min(limit, items.length))
  let hasMore = $derived(items.length > limit)

  // A new resetKey restarts the list. $effect.pre: runs before the DOM update, so new items never render at the old limit.
  $effect.pre(() => {
    void resetKey
    limit = INITIAL
  })

  // The observed box holds the button and the spacer, so it stays intersecting while the spacer is near the viewport.
  $effect(() => {
    watch(hasMore ? moreBox : null)
  })

  // Re-measure whenever the shown items or the limit change. Only those two are dependencies, as before.
  $effect(() => {
    void items
    void limit
    scheduleMeasure()
  })

  function watch(el) {
    if (!observer || el === watched) return
    if (watched) observer.unobserve(watched)
    watched = el
    if (el) observer.observe(el)
  }

  function onIntersect(entries) {
    if (entries.some((entry) => entry.isIntersecting)) grow(viewportBatch())
  }

  // Measure once the DOM shows the new items or limit.
  function scheduleMeasure() {
    tick().then(measure)
  }

  // One pass: column count, average row pitch (row height + row gap), and the spacer height.
  // Estimate: rows not mounted yet * pitch, scaled by SPACER_BIAS, minus the button block so the total matches once the list is complete.
  // The bias applies to the row estimate only, not to the button block.
  function measure() {
    if (destroyed || !wrapper) return
    const style = window.getComputedStyle(wrapper)
    cols = Math.max(1, (style.gridTemplateColumns || '').split(/\s+/).filter(Boolean).length)
    const rowGap = parseFloat(style.rowGap) || 0
    const loadedRows = Math.ceil(shown / cols)
    pitch = loadedRows > 0 && wrapper.offsetHeight > 0 ? (wrapper.offsetHeight + rowGap) / loadedRows : 0
    if (!pitch || !hasMore || !sentinel || !moreBox) {
      spacerHeight = 0
      return
    }
    const remainingRows = Math.ceil(items.length / cols) - loadedRows
    const more = window.getComputedStyle(moreBox)
    const buttonBlock = sentinel.offsetHeight + (parseFloat(more.marginTop) || 0) + (parseFloat(more.marginBottom) || 0)
    spacerHeight = Math.max(0, Math.round(remainingRows * pitch * SPACER_BIAS - buttonBlock))
  }

  // Cards that put the grid past the viewport plus the margin; if the box is already in reach, load the whole rest (the estimate may be short).
  function viewportBatch() {
    if (!pitch || !wrapper) return STEP
    if (moreBox && moreBox.getBoundingClientRect().bottom <= window.innerHeight + ROOT_MARGIN_PX) return items.length - limit
    const gridBottom = wrapper.getBoundingClientRect().bottom
    const neededPx = window.innerHeight + ROOT_MARGIN_PX - gridBottom
    const rows = Math.max(1, Math.ceil(neededPx / pitch))
    return Math.max(STEP, rows * cols)
  }

  // grow runs for observer hits (add = viewport batch) and button clicks (add = STEP).
  async function grow(add = STEP) {
    if (destroyed || limit >= items.length) return
    limit = Math.min(items.length, limit + add)
    await tick()
    if (destroyed) return
    // The observer reports only changes; re-observing reports the current position, so growth continues.
    if (observer && watched) {
      observer.unobserve(watched)
      observer.observe(watched)
    }
  }

  onMount(() => {
    measure()
    // Column count changes on window resize; the wrapper's size follows it.
    if (typeof ResizeObserver !== 'undefined' && wrapper) {
      resizeObserver = new ResizeObserver(() => measure())
      resizeObserver.observe(wrapper)
    }
  })

  onDestroy(() => {
    destroyed = true
    observer?.disconnect()
    resizeObserver?.disconnect()
    resizeObserver = null
    watched = null
  })
</script>

<div class={gridClass} bind:this={wrapper}>{#each items.slice(0, limit) as game (game.entryKey)}{@render children?.(game)}{/each}</div>
{#if hasMore}<div class="grid-more" bind:this={moreBox}><button bind:this={sentinel} type="button" class="secondary" onclick={() => grow()}>Show more ({shown} of {items.length})</button><div class="grid-spacer" aria-hidden="true" style="height: {spacerHeight}px"></div></div>{/if}
