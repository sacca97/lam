# gomobile bindings are called from native code by name.
-keep class go.** { *; }
-keep class lammobile.** { *; }
# Termux's view loads libtermux through JNI.
-keep class com.termux.** { *; }
