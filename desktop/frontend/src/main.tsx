import {createRoot} from 'react-dom/client';
import {App} from './App';
import {api,hostAPI} from './api/bridge';
import './app.css';
createRoot(document.getElementById('root')!).render(<App api={api} host={hostAPI}/>);
