<script>
  // Custom scrollbar for the document (runes, D15). The native scrollbar is hidden (style.css) because it would run the full
  // window height beside the title bar. This one is position: fixed and starts below the bar (app.css .app-scrollbar).
  // It only mirrors the page: wheel, keys, touch and find-in-page still scroll the document natively, so it is
  // aria-hidden and not focusable. Dragging the thumb or clicking the track just calls window.scrollTo.
  import {onMount} from 'svelte'

  // Thumb floor: stays grabbable on a very long list.
  const MIN_THUMB_PX = 48

  let trackEl = $state(null)
  let thumbEl = $state(null)
  let scrollable = $state(false)
  let dragging = $state(false)
  let thumbTop = $state(0)
  let thumbHeight = $state(MIN_THUMB_PX)

  let frame = 0
  // Drag origin: the pointer's y and the thumb's offset (in track pixels) when the press began.
  let dragStartY = 0
  let dragStartTop = 0

  function metrics() {
    const root = document.documentElement
    const viewport = root.clientHeight || window.innerHeight || 0
    const total = Math.max(root.scrollHeight || 0, viewport)
    const track = trackEl?.clientHeight || 0
    const scrollY = window.scrollY || root.scrollTop || 0
    const range = Math.max(0, total - viewport)
    const thumb = Math.min(track, Math.max(MIN_THUMB_PX, track && total ? (track * viewport) / total : 0))
    const travel = Math.max(0, track - thumb)
    return {viewport, total, track, scrollY, range, thumb, travel}
  }

  function update() {
    frame = 0
    const m = metrics()
    scrollable = m.range > 0 && m.track > 0
    thumbHeight = m.thumb
    const ratio = m.range > 0 ? Math.min(1, Math.max(0, m.scrollY / m.range)) : 0
    thumbTop = ratio * m.travel
  }

  // One update per frame however many scroll, resize or growth events arrive.
  function schedule() {
    if (frame) return
    frame = requestAnimationFrame(update)
  }

  function scrollToTop(top, range) {
    window.scrollTo({top: Math.min(range, Math.max(0, top)), behavior: 'instant'})
  }

  function onThumbDown(event) {
    if (event.button !== 0) return
    // preventDefault also stops the compatibility mouse events, so no text selection starts and Wails sees no mousedown.
    event.preventDefault()
    event.stopPropagation()
    dragging = true
    dragStartY = event.clientY
    dragStartTop = thumbTop
    try {
      thumbEl?.setPointerCapture?.(event.pointerId)
    } catch {
      // A pointer that is already gone cannot be captured; the drag simply ends on the next pointer event.
    }
  }

  // The thumb follows the pointer 1:1, so the page moves pointer delta * range / travel. The metrics are read again on every
  // move: the incremental grid can grow the page mid-drag.
  function onThumbMove(event) {
    if (!dragging) return
    // A move with no button held means the release was missed (pointerup never arrived, e.g. capture failed): end the drag.
    if (!(event.buttons & 1)) {
      endDrag(event)
      return
    }
    const m = metrics()
    if (m.travel <= 0) return
    const top = Math.min(m.travel, Math.max(0, dragStartTop + event.clientY - dragStartY))
    scrollToTop((top / m.travel) * m.range, m.range)
  }

  function endDrag(event) {
    if (!dragging) return
    dragging = false
    try {
      thumbEl?.releasePointerCapture?.(event.pointerId)
    } catch {
      // Already released (pointercancel or lost capture).
    }
  }

  // A press on the track outside the thumb pages one track height toward the press. The track is the visible part below
  // the title bar and above the bottom inset, so no content is skipped under the bar.
  function onTrackDown(event) {
    if (event.button !== 0 || event.target !== trackEl) return
    event.preventDefault()
    const m = metrics()
    const thumbTopOnScreen = trackEl.getBoundingClientRect().top + thumbTop
    const direction = event.clientY < thumbTopOnScreen ? -1 : 1
    scrollToTop(m.scrollY + direction * m.track, m.range)
  }

  onMount(() => {
    update()
    window.addEventListener('scroll', schedule, {passive: true})
    window.addEventListener('resize', schedule)
    // The page grows and shrinks (incremental grid batches, tab switches, banners): watch the content, not just the window.
    const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(schedule)
    observer?.observe(document.documentElement)
    observer?.observe(document.body)
    return () => {
      window.removeEventListener('scroll', schedule)
      window.removeEventListener('resize', schedule)
      observer?.disconnect()
      if (frame) cancelAnimationFrame(frame)
      frame = 0
    }
  })
</script>

<!-- A decorative mirror of the page's native scrolling: aria-hidden, no key handling, so the static-element warning does not apply. -->
<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="app-scrollbar" class:scrollable class:dragging bind:this={trackEl} aria-hidden="true" onpointerdown={onTrackDown}>
  <div
    class="app-scrollbar-thumb"
    bind:this={thumbEl}
    style:height="{thumbHeight}px"
    style:transform="translateY({thumbTop}px)"
    onpointerdown={onThumbDown}
    onpointermove={onThumbMove}
    onpointerup={endDrag}
    onpointercancel={endDrag}
    onlostpointercapture={endDrag}
  ></div>
</div>
