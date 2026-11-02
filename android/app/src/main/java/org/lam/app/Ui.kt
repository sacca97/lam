package org.lam.app

import android.content.Context
import android.graphics.Color
import android.graphics.Typeface
import android.graphics.drawable.GradientDrawable
import android.util.TypedValue
import android.view.Gravity
import android.view.View
import android.widget.Button
import android.widget.LinearLayout
import android.widget.TextView
import lammobile.Client

/** The connection shared by the screens. */
object Conn {
    @Volatile var client: Client? = null
    @Volatile var label: String = ""

    fun close() {
        val c = client
        client = null
        Pool.closeAll()
        if (c != null) Thread { runCatching { c.close() } }.start()
    }
}

object Theme {
    val bg = Color.parseColor("#111411")
    val surface = Color.parseColor("#1b201b")
    val text = Color.parseColor("#e6ebe6")
    val dim = Color.parseColor("#8a948a")
    val accent = Color.parseColor("#8cc88c")
    val danger = Color.parseColor("#e07070")
}

fun Context.dp(v: Int): Int =
    TypedValue.applyDimension(TypedValue.COMPLEX_UNIT_DIP, v.toFloat(), resources.displayMetrics).toInt()

fun Context.label(text: String, sizeSp: Float = 15f, color: Int = Theme.text, bold: Boolean = false) =
    TextView(this).apply {
        this.text = text
        setTextSize(TypedValue.COMPLEX_UNIT_SP, sizeSp)
        setTextColor(color)
        if (bold) setTypeface(typeface, Typeface.BOLD)
    }

fun Context.button(text: String, primary: Boolean = false, onClick: () -> Unit) =
    Button(this).apply {
        this.text = text
        isAllCaps = false
        setTextColor(if (primary) Theme.bg else Theme.text)
        background = GradientDrawable().apply {
            cornerRadius = dp(8).toFloat()
            setColor(if (primary) Theme.accent else Theme.surface)
        }
        setOnClickListener { onClick() }
    }

fun Context.dot(color: Int, sizeDp: Int = 10): View =
    View(this).apply {
        background = GradientDrawable().apply { shape = GradientDrawable.OVAL; setColor(color) }
        layoutParams = LinearLayout.LayoutParams(dp(sizeDp), dp(sizeDp)).apply {
            gravity = Gravity.CENTER_VERTICAL
            marginEnd = dp(12)
        }
    }

fun Context.screen(): LinearLayout =
    LinearLayout(this).apply {
        orientation = LinearLayout.VERTICAL
        setBackgroundColor(Theme.bg)
        setPadding(dp(20), dp(20), dp(20), dp(20))
    }
