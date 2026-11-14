package io.github.fermin214.moerise

import android.os.Bundle
import android.app.Application
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.ViewModelProvider
import androidx.lifecycle.viewModelScope
import io.github.fermin214.moerise.data.AndroidSecretStore
import io.github.fermin214.moerise.presentation.MoeriseApp
import io.github.fermin214.moerise.presentation.SessionController

class AppModel(application: Application) : AndroidViewModel(application) {
    val session = SessionController(AndroidSecretStore(application), BuildConfig.DEBUG, viewModelScope)
    override fun onCleared() { session.close() }
}

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        val model = ViewModelProvider(this)[AppModel::class.java]
        setContent { MoeriseApp(model.session) }
    }
}
