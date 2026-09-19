package com.mase.messenger.ui.components

import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.expandVertically
import androidx.compose.animation.shrinkVertically
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import com.mase.messenger.messaging.ConnectionUi

@Composable
fun ConnectionBanner(conn: ConnectionUi) {
    val (text, isError) = when (conn) {
        ConnectionUi.Idle       -> return
        ConnectionUi.Online     -> return
        ConnectionUi.Discovering -> "Ищем сервер в сети…" to false
        ConnectionUi.Connecting  -> "Подключаемся…" to false
        is ConnectionUi.Problem  -> conn.text to true
    }

    val bg = if (isError) MaterialTheme.colorScheme.errorContainer
             else MaterialTheme.colorScheme.primaryContainer
    val fg = if (isError) MaterialTheme.colorScheme.onErrorContainer
             else MaterialTheme.colorScheme.onPrimaryContainer

    AnimatedVisibility(visible = true, enter = expandVertically(), exit = shrinkVertically()) {
        Box(
            modifier = Modifier
                .fillMaxWidth()
                .background(bg)
                .padding(horizontal = 16.dp, vertical = 6.dp),
            contentAlignment = Alignment.Center
        ) {
            Text(
                text = text,
                style = MaterialTheme.typography.labelMedium,
                color = fg,
                textAlign = TextAlign.Center
            )
        }
    }
}
