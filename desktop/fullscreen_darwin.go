package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>

static void installFullScreenMenuItem(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		// AppKit otherwise adds "Show Tab Bar" to the View menu.
		[NSWindow setAllowsAutomaticWindowTabbing:NO];
		NSMenu *view = nil;
		for (NSMenuItem *top in NSApp.mainMenu.itemArray) {
			NSMenu *menu = top.submenu;
			if ([menu.title isEqualToString:@"View"]) view = menu;
			for (NSMenuItem *item in menu.itemArray) {
				if (item.action == NSSelectorFromString(@"enterFullScreenMode:")) [menu removeItem:item];
			}
		}
		if (view == nil || [view indexOfItemWithTarget:nil andAction:@selector(toggleFullScreen:)] >= 0) return;
		NSMenuItem *item = [[[NSMenuItem alloc] initWithTitle:@"Enter Full Screen" action:@selector(toggleFullScreen:) keyEquivalent:@"f"] autorelease];
		item.keyEquivalentModifierMask = NSEventModifierFlagControl | NSEventModifierFlagCommand;
		[view addItem:item];
		NSNotificationCenter *center = NSNotificationCenter.defaultCenter;
		[center addObserverForName:NSWindowDidEnterFullScreenNotification object:nil queue:NSOperationQueue.mainQueue usingBlock:^(NSNotification *note) {
			item.title = @"Exit Full Screen";
		}];
		[center addObserverForName:NSWindowDidExitFullScreenNotification object:nil queue:NSOperationQueue.mainQueue usingBlock:^(NSNotification *note) {
			item.title = @"Enter Full Screen";
		}];
	});
}
*/
import "C"

// installFullScreenMenuItem adds View > Enter Full Screen. Wails menu items can
// only call back into Go, and its built-in Window > Full Screen item can enter
// but never exit. A native toggleFullScreen: item toggles correctly, and the
// window notifications keep its title right however full screen was changed.
func installFullScreenMenuItem() { C.installFullScreenMenuItem() }
