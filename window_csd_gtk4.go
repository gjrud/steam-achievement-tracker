//go:build linux && cgo && !gtk3 && !android && !server && !purego

package main

// #cgo pkg-config: gtk4
// #include <gtk/gtk.h>
// #include <string.h>
//
// /* Results of sat_force_csd. */
// #define SAT_CSD_APPLIED 0
// #define SAT_CSD_NOT_NEEDED 1
// #define SAT_CSD_SKIPPED 2
//
// static int sat_force_csd(void *p) {
// 	if (p == NULL || gtk_get_minor_version() >= 16) {
// 		return SAT_CSD_NOT_NEEDED;
// 	}
// 	GtkWindow *w = GTK_WINDOW(p);
// 	GdkDisplay *d = gtk_widget_get_display(GTK_WIDGET(w));
// 	// Compare the type name instead of including the gtk4-wayland header.
// 	if (d == NULL || strcmp(G_OBJECT_TYPE_NAME(d), "GdkWaylandDisplay") != 0) {
// 		return SAT_CSD_NOT_NEEDED;
// 	}
// 	if (gtk_widget_get_realized(GTK_WIDGET(w)) || gtk_window_get_titlebar(w) != NULL) {
// 		return SAT_CSD_SKIPPED;
// 	}
// 	GtkWidget *bar = gtk_box_new(GTK_ORIENTATION_HORIZONTAL, 0);
// 	gtk_widget_set_visible(bar, FALSE);
// 	gtk_window_set_titlebar(w, bar);
// 	return SAT_CSD_APPLIED;
// }
import "C"

import (
	"github.com/wailsapp/wails/v3/pkg/application"
)

// forceClientDecorations gives the main window an empty, hidden titlebar so GTK treats it as client-decorated.
// Reason: GTK older than 4.16 on Wayland announces server-side decorations (through the KDE server-decoration protocol,
// so only compositors that support it, such as KWin, react; others like Mutter see no change) for any window that has
// no client-side titlebar, ignoring decorated=FALSE. So the compositor (KWin) draws its own title bar over the frameless window.
// decorated=FALSE still matters: it keeps the shadow width at 0 and the titlebar hidden. With a titlebar set, GTK
// announces client-side decorations and the compositor draws nothing.
// Call it before Show, while the window is still unrealized. A Wails PrimaryMenu menu added later would replace or
// clear this titlebar and the compositor title bar would return.
// The C side does nothing on GTK 4.16 or later, on non-Wayland displays, and when the window is already realized or
// has a titlebar. A nil window is handled there too (NativeWindow returns nil when the window is gone). Only the
// "already realized or has a titlebar" case is logged, because there the title bar may return.
func forceClientDecorations(app *application.App, win *application.WebviewWindow) {
	var result C.int
	application.InvokeSync(func() {
		result = C.sat_force_csd(win.NativeWindow())
	})
	if result == C.SAT_CSD_SKIPPED {
		app.Logger.Warn("could not set client-side titlebar: window already realized or has a titlebar; a compositor title bar may appear")
	}
}
