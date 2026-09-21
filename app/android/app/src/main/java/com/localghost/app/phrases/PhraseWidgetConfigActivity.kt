package com.localghost.app.phrases

import android.app.Activity
import android.appwidget.AppWidgetManager
import android.content.Intent
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import com.localghost.app.ui.GhostButton
import com.localghost.app.ui.GhostScaffold
import com.localghost.app.ui.WidgetLookEditor
import com.localghost.app.ui.theme.GhostTextDim
import com.localghost.app.ui.theme.LocalGhostTheme
import com.localghost.app.ui.theme.TerminalGreen

/**
 * The widget's own settings screen. The launcher opens it when the widget is placed (unless it
 * takes the configuration_optional shortcut) and again from long-press › settings; the same
 * editor sits under PHRASES › widget › look. Every change is saved and drawn on the placed
 * widgets at once, so the person sees the result behind the sheet; DONE hands the widget id
 * back to the launcher, which is what makes a first placement stick.
 */
class PhraseWidgetConfigActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val id = intent?.getIntExtra(AppWidgetManager.EXTRA_APPWIDGET_ID, AppWidgetManager.INVALID_APPWIDGET_ID)
            ?: AppWidgetManager.INVALID_APPWIDGET_ID
        // Until DONE, backing out means "do not place" to the launcher (a first placement) or "keep
        // what was" (a reconfigure , the look is already saved live, so nothing is lost either way).
        setResult(Activity.RESULT_CANCELED, Intent().putExtra(AppWidgetManager.EXTRA_APPWIDGET_ID, id))
        setContent {
            LocalGhostTheme {
                GhostScaffold { pad ->
                    Column(Modifier.fillMaxSize().padding(pad).padding(20.dp).verticalScroll(rememberScrollState())) {
                        Text("> WIDGET", color = TerminalGreen, style = MaterialTheme.typography.titleMedium)
                        Spacer(Modifier.height(4.dp))
                        Text("how the phrase looks on your home and lock screen · changes show on the widget as you make them",
                            color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                        Spacer(Modifier.height(16.dp))
                        WidgetLookEditor()
                        Spacer(Modifier.height(20.dp))
                        GhostButton("DONE", onClick = {
                            Thread { PhraseSurface.updateWidgets(applicationContext) }.start()
                            setResult(Activity.RESULT_OK, Intent().putExtra(AppWidgetManager.EXTRA_APPWIDGET_ID, id))
                            finish()
                        }, modifier = Modifier.fillMaxWidth())
                    }
                }
            }
        }
    }
}
