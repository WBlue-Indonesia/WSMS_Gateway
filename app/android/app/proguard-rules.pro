# WSMS Sender — R8/ProGuard keep rules.
#
# Release minification is currently disabled (see build.gradle.kts) because R8 renamed
# the flutter_foreground_task Service, breaking the foreground service. These rules keep
# the build safe if minification is ever re-enabled.

# flutter_foreground_task: its Service + RestartReceiver are started by class name from
# the manifest, so they must never be renamed or stripped.
-keep class com.pravera.flutter_foreground_task.** { *; }

# This app's own Kotlin (platform-channel handler, Application, engine setup).
-keep class id.wblue.wsms_sender.** { *; }

# Flutter embedding + plugin channels.
-keep class io.flutter.embedding.** { *; }
-keep class io.flutter.plugin.** { *; }
-keep class io.flutter.plugins.** { *; }

# Firebase Cloud Messaging (background message service).
-keep class com.google.firebase.** { *; }
-dontwarn com.google.firebase.**
