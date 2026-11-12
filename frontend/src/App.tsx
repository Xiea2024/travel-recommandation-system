import { Route, Routes } from 'react-router-dom';
import Home from './pages/Home';
import CityPage from './pages/CityPage';
import SearchPage from './pages/SearchPage';
import RouteDetail from './pages/RouteDetail';

export default function App() {
  return (
    <Routes>
      <Route path="/" element={<Home />} />
      <Route path="/city/:cityId" element={<CityPage />} />
      <Route path="/search" element={<SearchPage />} />
      <Route path="/route/:routeId" element={<RouteDetail />} />
    </Routes>
  );
}
