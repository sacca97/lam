package org.lam.app

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.Color

/** The same greens as the desktop window: calm, with colour only where something needs a look. */
private val Dark = darkColorScheme(
    primary = Color(0xFF8CC88C),
    onPrimary = Color(0xFF0E1A0E),
    background = Color(0xFF111411),
    onBackground = Color(0xFFE6EBE6),
    surface = Color(0xFF111411),
    onSurface = Color(0xFFE6EBE6),
    onSurfaceVariant = Color(0xFF8A948A),
    surfaceContainer = Color(0xFF1B201B),
    surfaceContainerHigh = Color(0xFF232923),
    outlineVariant = Color(0xFF2E352E),
    error = Color(0xFFE07070),
    tertiary = Color(0xFFE9A06B),
)

private val Light = lightColorScheme(
    primary = Color(0xFF2F7A3A),
    onPrimary = Color.White,
    background = Color(0xFFF6F8F5),
    onBackground = Color(0xFF181C18),
    surface = Color(0xFFF6F8F5),
    onSurface = Color(0xFF181C18),
    onSurfaceVariant = Color(0xFF5B665B),
    surfaceContainer = Color(0xFFEAEEE8),
    surfaceContainerHigh = Color(0xFFDFE5DD),
    outlineVariant = Color(0xFFC8D0C6),
    error = Color(0xFFB3382F),
    tertiary = Color(0xFFB0581E),
)

@Composable
fun LamTheme(content: @Composable () -> Unit) {
    MaterialTheme(colorScheme = if (isSystemInDarkTheme()) Dark else Light, content = content)
}
