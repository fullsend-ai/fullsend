import React, {useEffect, useState} from 'react';

export default function ReadingProgress(): React.JSX.Element {
  const [width, setWidth] = useState(0);

  useEffect(() => {
    function onScroll() {
      const {scrollTop, scrollHeight, clientHeight} = document.documentElement;
      const total = scrollHeight - clientHeight;
      setWidth(total > 0 ? (scrollTop / total) * 100 : 0);
    }
    window.addEventListener('scroll', onScroll, {passive: true});
    return () => window.removeEventListener('scroll', onScroll);
  }, []);

  return <div className="reading-progress" style={{width: `${width}%`}} />;
}
